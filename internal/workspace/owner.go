package workspace

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/getan/golder/internal/cli/workspacecmd"
	"github.com/getan/golder/internal/privatepath"
)

const ownerFrameLimit = 8 << 10

// ownerRecord is private state, not a public protocol result. Its token is
// scoped to one run and only authorizes lifecycle operations on that run.
type ownerRecord struct {
	Socket        string `json:"socket"`
	Control       string `json:"control"`
	Session       string `json:"session"`
	Project       string `json:"project"`
	Instance      string `json:"instance"`
	Token         string `json:"token"`
	Version       string `json:"version"`
	PID           int    `json:"pid"`
	ConfigPath    string `json:"config"`
	StateDir      string `json:"state_dir"`
	LogFile       string `json:"log"`
	StopRequested bool   `json:"stop_requested,omitempty"`
}

type ownerReply struct {
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
	Instance string `json:"instance,omitempty"`
	Version  string `json:"version,omitempty"`
	PID      int    `json:"pid,omitempty"`
}

type ownerRequest struct {
	Action string `json:"action"`
	Token  string `json:"token"`
}

type ownerServer struct {
	listener net.Listener
	record   ownerRecord
	file     string
	cancel   context.CancelFunc
	once     sync.Once
	wg       sync.WaitGroup
	mu       sync.Mutex
	conns    map[net.Conn]struct{}
	done     chan struct{}
	stopping atomic.Bool
}

// ownerPaths uses a hash rather than appending to the daemon socket: a custom
// socket may already occupy the whole Unix-domain path budget.
func ownerPaths(opts workspacecmd.Options) (socket, record string) {
	sum := sha256.Sum256([]byte(opts.Socket))
	key := hex.EncodeToString(sum[:8])
	socket = filepath.Join(filepath.Dir(opts.Socket), "o-"+key+".sock")
	record = filepath.Join(opts.SessionDir, "owner-"+key+".json")
	return socket, record
}

func startOwner(ctx context.Context, opts workspacecmd.Options, version string, cancel context.CancelFunc) (*ownerServer, error) {
	socket, file := ownerPaths(opts)
	if len(socket) > 103 {
		return nil, fmt.Errorf("workspace: lifecycle socket %q exceeds the 103-byte Unix limit; use a shorter --socket directory", socket)
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return nil, err
	}
	var instance [16]byte
	if _, err := rand.Read(instance[:]); err != nil {
		return nil, err
	}
	// Recover only an owner-owned, dead socket. A live endpoint, a symlink or a
	// regular file is never unlinked just because it occupies the desired name.
	if st, err := os.Lstat(socket); err == nil {
		if st.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("workspace: lifecycle path %q is not a socket", socket)
		}
		conn, dialErr := net.DialTimeout("unix", socket, 150*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			return nil, errors.New("workspace: the lifecycle socket is already in use")
		}
		if !refusedSocket(dialErr) {
			return nil, fmt.Errorf("workspace: lifecycle endpoint could not be safely probed: %w", dialErr)
		}
		if err := os.Remove(socket); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	ln, err := net.Listen("unix", socket)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		_ = ln.Close()
		return nil, err
	}
	record := ownerRecord{
		Socket: opts.Socket, Control: socket, Session: opts.Session, Project: opts.Cwd,
		Instance: hex.EncodeToString(instance[:]), Token: hex.EncodeToString(secret[:]),
		Version: version, PID: os.Getpid(),
		ConfigPath: opts.ConfigPath, StateDir: opts.StateDir, LogFile: opts.LogFile,
	}
	if err := writeOwner(file, record); err != nil {
		_ = ln.Close()
		return nil, err
	}
	s := &ownerServer{listener: ln, record: record, file: file, cancel: cancel, conns: make(map[net.Conn]struct{}), done: make(chan struct{})}
	s.wg.Add(1)
	go s.serve()
	go func() {
		select {
		case <-ctx.Done():
			// The daemon's Done, not context cancellation, orders Close and removal
			// of the record: a stop is not complete until panes/state are saved.
			_ = s.listener.Close()
		case <-s.done:
		}
	}()
	return s, nil
}

func writeOwner(path string, record ownerRecord) error {
	if err := privatepath.EnsureDir(filepath.Dir(path)); err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".workspace-owner-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(append(data, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readOwner(opts workspacecmd.Options) (ownerRecord, error) {
	socket, file := ownerPaths(opts)
	data, err := privatepath.ReadFile(file, 0o600, ownerFrameLimit)
	if err != nil {
		return ownerRecord{}, err
	}
	var record ownerRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return ownerRecord{}, fmt.Errorf("workspace: invalid ownership record: %w", err)
	}
	if record.Socket != opts.Socket || record.Control != socket || record.Session != opts.Session || record.Project != opts.Cwd || len(record.Token) != 64 || len(record.Instance) != 32 {
		return ownerRecord{}, errors.New("workspace: ownership record does not match this project, session and socket")
	}
	return record, nil
}

func (s *ownerServer) serve() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if len(s.conns) >= 8 {
			s.mu.Unlock()
			_ = conn.Close()
			continue
		}
		s.conns[conn] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go s.handle(conn)
	}
}

func (s *ownerServer) handle(conn net.Conn) {
	defer s.wg.Done()
	defer func() {
		_ = conn.Close()
		s.mu.Lock()
		delete(s.conns, conn)
		s.mu.Unlock()
	}()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	dec := json.NewDecoder(io.LimitReader(conn, ownerFrameLimit))
	dec.DisallowUnknownFields()
	var request ownerRequest
	if err := dec.Decode(&request); err != nil {
		_ = json.NewEncoder(conn).Encode(ownerReply{Error: "invalid lifecycle request"})
		return
	}
	if subtle.ConstantTimeCompare([]byte(request.Token), []byte(s.record.Token)) != 1 {
		_ = json.NewEncoder(conn).Encode(ownerReply{Error: "workspace ownership proof refused"})
		return
	}
	if request.Action != "status" && request.Action != "stop" {
		_ = json.NewEncoder(conn).Encode(ownerReply{Error: "unknown lifecycle action"})
		return
	}
	if request.Action == "stop" {
		record := s.record
		record.StopRequested = true
		if err := writeOwner(s.file, record); err != nil {
			_ = json.NewEncoder(conn).Encode(ownerReply{Error: "could not record the lifecycle transition"})
			return
		}
	}
	if err := json.NewEncoder(conn).Encode(ownerReply{
		OK: true, Instance: s.record.Instance, Version: s.record.Version, PID: s.record.PID,
	}); err == nil && request.Action == "stop" {
		// Reply before cancellation: the client must know its stop was accepted.
		s.stopping.Store(true)
		s.cancel()
	}
}

func (s *ownerServer) Close() {
	s.once.Do(func() {
		close(s.done)
		_ = s.listener.Close()
		s.mu.Lock()
		for conn := range s.conns {
			_ = conn.Close()
		}
		s.mu.Unlock()
		s.wg.Wait()
		// A later run may have replaced a stale record; never delete its proof.
		data, err := privatepath.ReadFile(s.file, 0o600, ownerFrameLimit)
		var current ownerRecord
		if err == nil && json.Unmarshal(data, &current) == nil && current.Instance == s.record.Instance {
			_ = os.Remove(s.file)
		}
	})
}

func callOwner(ctx context.Context, record ownerRecord, action string) (ownerReply, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", record.Control)
	if err != nil {
		return ownerReply{}, err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()
	if err := json.NewEncoder(conn).Encode(ownerRequest{Action: action, Token: record.Token}); err != nil {
		return ownerReply{}, err
	}
	var reply ownerReply
	if err := json.NewDecoder(io.LimitReader(conn, ownerFrameLimit)).Decode(&reply); err != nil {
		return ownerReply{}, err
	}
	if !reply.OK || reply.Instance != record.Instance || reply.PID != record.PID {
		return ownerReply{}, errors.New("workspace: lifecycle endpoint refused the ownership proof or answered for a different instance")
	}
	return reply, nil
}
