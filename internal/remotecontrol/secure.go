package remotecontrol

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/coder/websocket"
)

//go:embed webv2/*
var secureFiles embed.FS

const (
	securePrefix      = "/remote/v2/"
	secureCookie      = "golder_remote_v2"
	securePairTTL     = 2 * time.Minute
	secureMaxFrame    = 128 << 10
	secureMaxInput    = 64 << 10
	secureMaxRequests = 4096
)

// MobileMessage is an explicitly projected transcript item. It intentionally
// does not contain tool arguments, attachments, credentials, or UI internals.
type MobileMessage struct {
	ID   string `json:"id"`
	Role string `json:"role"`
	Text string `json:"text"`
}

type MobileNeed struct {
	Capability string `json:"capability"`
	Path       string `json:"path,omitempty"`
	Directory  string `json:"directory,omitempty"`
	Target     string `json:"target,omitempty"`
}

type MobileChoice struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// MobileApproval is a presentation of a still-pending original gate request.
// The adapter, not the client, owns the original request and offered choices.
type MobileApproval struct {
	ID            string         `json:"id"`
	RunID         string         `json:"run_id,omitempty"`
	ToolCallID    string         `json:"tool_call_id,omitempty"`
	Kind          string         `json:"kind"`
	Tool          string         `json:"tool"`
	Summary       string         `json:"summary"`
	Justification string         `json:"justification,omitempty"`
	Rationale     string         `json:"rationale,omitempty"`
	Source        string         `json:"source,omitempty"`
	Level         string         `json:"level,omitempty"`
	Mode          string         `json:"mode,omitempty"`
	Origin        string         `json:"origin,omitempty"`
	TrustDir      string         `json:"trust_dir,omitempty"`
	Needs         []MobileNeed   `json:"needs,omitempty"`
	Choices       []MobileChoice `json:"choices"`
}

// MobileSnapshot is a complete, allowlisted view of one bound live session.
type MobileSnapshot struct {
	SessionID  string           `json:"session_id"`
	Frontend   string           `json:"frontend"`
	Project    string           `json:"project"`
	Title      string           `json:"title,omitempty"`
	Permission string           `json:"permission"`
	Running    bool             `json:"running"`
	Truncated  bool             `json:"truncated,omitempty"`
	Messages   []MobileMessage  `json:"messages"`
	Approvals  []MobileApproval `json:"approvals"`
}

// MobileSession is implemented by a frontend adapter. Snapshot must be safe to
// call concurrently; Submit/Answer must retain the frontend's original gates.
// Disconnected must invalidate unanswered remotely visible approvals.
type MobileSession interface {
	Snapshot() (MobileSnapshot, error)
	Submit(context.Context, string) error
	Answer(context.Context, string, string) error
	Disconnected()
	Done() <-chan struct{}
}

// PairRequest is displayed only to the local operator. DeviceName is not an
// authenticated identity; the human must compare Code on both devices.
type PairRequest struct {
	ID         string
	Code       string
	DeviceName string
}

type securePair struct {
	id, code, name string
	proof          [32]byte
	approved       bool
	denied         bool
}

type secureRequest struct {
	digest [32]byte
	result string
}

type secureSnapshotEvent struct {
	seq  uint64
	data []byte
	at   time.Time
}

// SecureServer serves exactly one session behind a verified Tailnet HTTPS
// proxy. The listener itself is never exposed on a non-loopback interface.
type SecureServer struct {
	mu           sync.Mutex
	opMu         sync.Mutex
	captureMu    sync.Mutex
	identity     ServeIdentity
	session      MobileSession
	sessionID    string
	instance     string
	epoch        string
	pairHash     [32]byte
	expires      time.Time
	pair         *securePair
	pairWindow   time.Time
	pairAttempts int
	credential   [32]byte
	paired       bool
	csrf         string
	client       *websocket.Conn
	reserved     bool
	requests     map[string]secureRequest
	seq          uint64
	lastSnapshot []byte
	history      []secureSnapshotEvent
	historyBytes int
	updates      chan PairRequest
	stop         chan struct{}
	stopped      bool
	listener     net.Listener
	http         *http.Server
	verify       func(context.Context) (ServeIdentity, error)
}

// StartSecure verifies the existing Serve configuration and binds the fixed
// loopback port. It never configures Tailscale or falls back to LAN HTTP.
// The returned URL is a secret and must be shown only in an operator-only UI.
func StartSecure(ctx context.Context, session MobileSession) (*SecureServer, string, error) {
	identity, err := VerifyTailscaleServe(ctx)
	if err != nil {
		return nil, "", err
	}
	s, pairURL, err := newSecureServer(identity, session, VerifyTailscaleServe)
	if err != nil {
		return nil, "", err
	}
	ln, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", SecurePort))
	if err != nil {
		return nil, "", fmt.Errorf("remote control loopback port is unavailable: %w", err)
	}
	s.listener = ln
	s.http = &http.Server{Handler: s, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() { _ = s.http.Serve(ln) }()
	go s.monitor()
	go s.captureSnapshots()
	return s, pairURL, nil
}

func newSecureServer(identity ServeIdentity, session MobileSession, verify func(context.Context) (ServeIdentity, error)) (*SecureServer, string, error) {
	u, err := url.Parse(identity.Origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || identity.NodeID == "" || identity.TailnetID == "" || session == nil {
		return nil, "", errors.New("invalid verified remote-control identity")
	}
	snapshot, err := session.Snapshot()
	if err != nil || snapshot.SessionID == "" {
		return nil, "", errors.New("no active session to share")
	}
	instance, err := randHex(16)
	if err != nil {
		return nil, "", err
	}
	epoch, err := randHex(16)
	if err != nil {
		return nil, "", err
	}
	token, err := randHex(tokenBytes)
	if err != nil {
		return nil, "", err
	}
	s := &SecureServer{
		identity: identity, session: session, sessionID: snapshot.SessionID, instance: instance, epoch: epoch,
		pairHash: sha256.Sum256([]byte(token)), expires: time.Now().Add(securePairTTL),
		requests: make(map[string]secureRequest), updates: make(chan PairRequest, 1),
		stop: make(chan struct{}), verify: verify,
	}
	return s, identity.Origin + securePrefix + "#token=" + token, nil
}

func (s *SecureServer) PairRequests() <-chan PairRequest { return s.updates }

// ConfirmPair is callable only from a local operator UI. The code is compared
// again so a stale UI cannot approve a later pairing attempt.
func (s *SecureServer) ConfirmPair(id, code string, allow bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.pair == nil || s.pair.id != id || s.pair.code != code || s.pair.approved || s.pair.denied || time.Now().After(s.expires) {
		return errors.New("pairing is no longer pending")
	}
	s.pair.approved = allow
	s.pair.denied = !allow
	return nil
}

func (s *SecureServer) monitor() {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-s.session.Done():
			_ = s.Stop(context.Background())
			return
		case <-ticker.C:
			s.mu.Lock()
			expired := !s.paired && time.Now().After(s.expires)
			s.mu.Unlock()
			if expired {
				_ = s.Stop(context.Background())
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			id, err := s.verify(ctx)
			cancel()
			if err != nil || id != s.identity {
				_ = s.Stop(context.Background())
				return
			}
		}
	}
}

// Revoke immediately invalidates pairing and closes the client connection.
// An operation already admitted before revocation may have an unknown outcome.
func (s *SecureServer) Revoke() {
	s.mu.Lock()
	s.paired = false
	s.pairHash = [32]byte{}
	s.credential = [32]byte{}
	s.csrf = ""
	s.pair = nil
	conn := s.client
	s.mu.Unlock()
	if conn != nil {
		_ = conn.CloseNow()
	}
}

func (s *SecureServer) Stop(ctx context.Context) error {
	s.Revoke()
	s.opMu.Lock()
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		s.opMu.Unlock()
		return nil
	}
	s.stopped = true
	s.paired = false
	s.pairHash = [32]byte{}
	s.credential = [32]byte{}
	s.csrf = ""
	s.pair = nil
	s.requests = nil
	s.lastSnapshot = nil
	s.history = nil
	s.historyBytes = 0
	conn, server := s.client, s.http
	close(s.stop)
	s.mu.Unlock()
	s.opMu.Unlock()
	if conn != nil {
		_ = conn.CloseNow()
	}
	if server != nil {
		return server.Shutdown(ctx)
	}
	return nil
}

func (s *SecureServer) Connected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.stopped && s.paired && s.client != nil
}

func (s *SecureServer) Done() <-chan struct{} { return s.stop }

func (s *SecureServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self' wss://"+strings.TrimPrefix(s.identity.Origin, "https://")+"; img-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	u, _ := url.Parse(s.identity.Origin)
	if !strings.EqualFold(r.Host, u.Host) && !(u.Port() == "" && strings.EqualFold(r.Host, u.Host+":443")) {
		http.Error(w, "invalid host", http.StatusForbidden)
		return
	}
	s.mu.Lock()
	stopped := s.stopped
	s.mu.Unlock()
	if stopped {
		http.Error(w, "remote control stopped", http.StatusGone)
		return
	}
	if (r.Method != http.MethodGet || r.URL.Path == securePrefix+"events") && r.Header.Get("Origin") != s.identity.Origin {
		http.Error(w, "invalid origin", http.StatusForbidden)
		return
	}
	switch r.URL.Path {
	case securePrefix, securePrefix + "app.js", securePrefix + "style.css":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, securePrefix)
		if name == "" {
			name = "index.html"
		}
		f, _ := fs.Sub(secureFiles, "webv2")
		data, err := fs.ReadFile(f, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(name, ".html") {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		}
		if strings.HasSuffix(name, ".js") {
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		}
		if strings.HasSuffix(name, ".css") {
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		}
		_, _ = w.Write(data)
	case securePrefix + "pair/start":
		s.handlePairStart(w, r)
	case securePrefix + "pair/status":
		s.handlePairStatus(w, r)
	case securePrefix + "bootstrap":
		if r.Method != http.MethodGet || !s.authenticated(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		s.mu.Lock()
		csrf := s.csrf
		s.mu.Unlock()
		writeJSON(w, map[string]string{"csrf": csrf, "instance_id": s.instance, "epoch": s.epoch})
	case securePrefix + "snapshot":
		if r.Method != http.MethodGet || !s.authenticated(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		frame, err := s.snapshotFrame()
		if err != nil {
			http.Error(w, "session unavailable", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, frame)
	case securePrefix + "events":
		s.handleSecureWS(w, r)
	case securePrefix + "disconnect":
		if r.Method != http.MethodPost || !s.authenticated(r) || !s.validCSRF(r.Header.Get("X-Golder-CSRF")) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		s.Revoke()
		go func() { _ = s.Stop(context.Background()) }()
		writeJSON(w, map[string]string{"status": "stopped"})
	default:
		http.NotFound(w, r)
	}
}

func readSecureJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if r.Method != http.MethodPost || err != nil || contentType != "application/json" {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if dec.Decode(v) != nil || dec.Decode(&struct{}{}) != io.EOF {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *SecureServer) handlePairStart(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token      string `json:"token"`
		DeviceName string `json:"device_name"`
	}
	if !readSecureJSON(w, r, &in) {
		return
	}
	s.mu.Lock()
	now := time.Now()
	if s.pairWindow.IsZero() || now.Sub(s.pairWindow) >= time.Minute {
		s.pairWindow, s.pairAttempts = now, 0
	}
	s.pairAttempts++
	limited := s.pairAttempts > 30
	s.mu.Unlock()
	if limited {
		http.Error(w, "too many pairing attempts", http.StatusTooManyRequests)
		return
	}
	if len(in.Token) != tokenBytes*2 || len(in.DeviceName) > 80 || strings.IndexFunc(in.DeviceName, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) >= 0 {
		http.Error(w, "invalid pairing", http.StatusUnauthorized)
		return
	}
	hash := sha256.Sum256([]byte(in.Token))
	id, e1 := randHex(16)
	proof, e2 := randHex(tokenBytes)
	code, e3 := randHex(3)
	if e1 != nil || e2 != nil || e3 != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	s.mu.Lock()
	if s.stopped || s.paired || s.pair != nil || time.Now().After(s.expires) || subtle.ConstantTimeCompare(hash[:], s.pairHash[:]) != 1 {
		s.mu.Unlock()
		http.Error(w, "invalid or expired pairing", http.StatusUnauthorized)
		return
	}
	s.pairHash = [32]byte{}
	s.pair = &securePair{id: id, code: code, name: strings.TrimSpace(in.DeviceName), proof: sha256.Sum256([]byte(proof))}
	request := PairRequest{ID: id, Code: code, DeviceName: s.pair.name}
	s.mu.Unlock()
	select {
	case s.updates <- request:
	default:
	}
	writeJSON(w, map[string]string{"pair_id": id, "proof": proof, "code": code})
}

func (s *SecureServer) handlePairStatus(w http.ResponseWriter, r *http.Request) {
	var in struct {
		PairID string `json:"pair_id"`
		Proof  string `json:"proof"`
	}
	if !readSecureJSON(w, r, &in) {
		return
	}
	proof := sha256.Sum256([]byte(in.Proof))
	s.mu.Lock()
	if s.stopped || s.pair == nil || s.pair.id != in.PairID || time.Now().After(s.expires) || subtle.ConstantTimeCompare(proof[:], s.pair.proof[:]) != 1 {
		s.mu.Unlock()
		http.Error(w, "invalid or expired pairing", http.StatusUnauthorized)
		return
	}
	if s.pair.denied {
		s.pair = nil
		s.mu.Unlock()
		http.Error(w, "pairing rejected", http.StatusForbidden)
		return
	}
	if !s.pair.approved {
		s.mu.Unlock()
		writeJSON(w, map[string]string{"status": "waiting"})
		return
	}
	credential, e1 := randHex(tokenBytes)
	csrf, e2 := randHex(tokenBytes)
	if e1 != nil || e2 != nil {
		s.mu.Unlock()
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	s.credential = sha256.Sum256([]byte(credential))
	s.csrf, s.paired, s.pair = csrf, true, nil
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: secureCookie, Value: credential, Path: securePrefix, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	writeJSON(w, map[string]string{"status": "paired"})
}

func (s *SecureServer) authenticated(r *http.Request) bool {
	cookie, err := r.Cookie(secureCookie)
	if err != nil || len(cookie.Value) != tokenBytes*2 {
		return false
	}
	hash := sha256.Sum256([]byte(cookie.Value))
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.stopped && s.paired && subtle.ConstantTimeCompare(hash[:], s.credential[:]) == 1
}

func (s *SecureServer) validCSRF(value string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.stopped && s.csrf != "" && subtle.ConstantTimeCompare([]byte(value), []byte(s.csrf)) == 1
}
