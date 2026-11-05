package computeruse

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/getan/golder/internal/tuiosctl"
)

// OperatorPanePrefix is reserved for the person's desktop-control view. Pane
// tools must not inject input into it or capture its sensitive contents.
const OperatorPanePrefix = "golder-computer-use-"

var operatorPanes sync.Map

func IsOperatorPane(windowID string) bool {
	_, ok := operatorPanes.Load(windowID)
	return ok
}

// PaneHandle owns the helper pane and its private connections. Closing it
// revokes the target immediately, including when the model is still running.
type PaneHandle struct {
	windowID string
	session  *Session
	cancel   context.CancelFunc
	conns    []net.Conn
	done     chan struct{}
	once     sync.Once
}

func (h *PaneHandle) WindowID() string {
	if h == nil {
		return ""
	}
	return h.windowID
}

func (h *PaneHandle) Done() <-chan struct{} { return h.done }

func (h *PaneHandle) Close() {
	if h == nil {
		return
	}
	h.once.Do(func() {
		h.session.Stop()
		h.cancel()
		for _, conn := range h.conns {
			_ = conn.Close()
		}
	})
}

// LaunchOperatorPane opens a helper next to this exact agent pane. The target
// must already have been selected and granted by the operator. Separate sockets
// let pause/stop interrupt a slow capture or action rather than queue behind it.
// A failure to prove all peers as the same process in the new pane fails
// closed; no model-facing endpoint is created.
func LaunchOperatorPane(ctx context.Context, session *Session, cwd string) (*PaneHandle, error) {
	if session == nil || !session.Status().Active || !tuiosctl.Available() {
		return nil, errors.New("computer pane requires an active session and a local tuios workspace")
	}
	client, err := tuiosctl.Dial(ctx, tuiosctl.FromEnv())
	if err != nil {
		return nil, fmt.Errorf("computer pane: %w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	dir, err := os.MkdirTemp("", "golder-cu-")
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	socket := filepath.Join(dir, "pane.sock")
	cleanup := func() {
		_ = os.Remove(socket)
		_ = os.Remove(dir)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		cleanup()
		_ = client.Close()
		return nil, fmt.Errorf("computer pane private socket: %w", err)
	}
	if err = os.Chmod(socket, 0o600); err != nil {
		_ = listener.Close()
		cleanup()
		_ = client.Close()
		return nil, err
	}
	name := OperatorPanePrefix + rand.Text()[:10]
	created, err := client.NewWindowInCallerWorkspace(ctx, tuiosctl.NewWindowParams{
		Name: name, Cwd: cwd, Command: []string{executable, "computer-pane", socket}, CloseOnExit: true,
	})
	if err != nil || created.WindowID == "" {
		_ = listener.Close()
		cleanup()
		_ = client.Close()
		if err == nil {
			err = errors.New("tuios did not return the new pane identity")
		}
		return nil, fmt.Errorf("computer pane launch: %w", err)
	}
	operatorPanes.Store(created.WindowID, true)
	closeCreated := func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if client.CloseWindow(closeCtx, created.WindowID) == nil {
			operatorPanes.Delete(created.WindowID)
		}
		_ = client.Close()
	}
	if unixListener, ok := listener.(*net.UnixListener); ok {
		_ = unixListener.SetDeadline(time.Now().Add(15 * time.Second))
	}
	stopAccept := context.AfterFunc(ctx, func() { _ = listener.Close() })
	var conns []net.Conn
	var firstPID int
	var firstStart uint64
	for len(conns) < 3 {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			err = acceptErr
			break
		}
		pid, start, identityErr := operatorPeerIdentity(conn)
		if identityErr == nil && len(conns) > 0 && (pid != firstPID || start != firstStart) {
			identityErr = errors.New("computer pane connections came from different processes")
		}
		if identityErr == nil {
			identityErr = verifyOperatorPanePeer(ctx, client, created.WindowID, pid, start)
		}
		if identityErr != nil {
			_ = conn.Close()
			err = identityErr
			break
		}
		firstPID, firstStart = pid, start
		conns = append(conns, conn)
	}
	stopAccept()
	_ = listener.Close()
	cleanup()
	if len(conns) != 3 || ctx.Err() != nil {
		for _, conn := range conns {
			_ = conn.Close()
		}
		closeCreated()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("computer pane handshake: %w", err)
	}
	pane, err := NewPane(session, io.Discard, 1, false)
	if err != nil {
		for _, conn := range conns {
			_ = conn.Close()
		}
		closeCreated()
		return nil, err
	}
	lifetime, cancel := context.WithCancel(context.Background())
	handle := &PaneHandle{windowID: created.WindowID, session: session, cancel: cancel, conns: conns, done: make(chan struct{})}
	go func() {
		defer close(handle.done)
		results := make(chan error, len(conns))
		for _, conn := range conns {
			go func() { results <- ServeOperatorPane(lifetime, conn, pane) }()
		}
		<-results
		handle.Close()
		for range len(conns) - 1 {
			<-results
		}
		_ = session.Close()
		closeCreated()
	}()
	return handle, nil
}
