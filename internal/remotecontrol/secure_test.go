package remotecontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/getan/golder/internal/judge"
)

type fakeMobileSession struct {
	mu          sync.Mutex
	snapshot    MobileSnapshot
	inputs      []string
	answers     []string
	disconnects int
	done        chan struct{}
}

func (f *fakeMobileSession) Snapshot() (MobileSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snapshot, nil
}
func (f *fakeMobileSession) Submit(_ context.Context, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inputs = append(f.inputs, text)
	return nil
}
func (f *fakeMobileSession) Answer(_ context.Context, id, choice string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers = append(f.answers, id+":"+choice)
	return nil
}
func (f *fakeMobileSession) Disconnected() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disconnects++
}
func (f *fakeMobileSession) Done() <-chan struct{} { return f.done }

func newTestSecure(t *testing.T) (*SecureServer, *fakeMobileSession, string) {
	t.Helper()
	f := &fakeMobileSession{snapshot: MobileSnapshot{SessionID: "session-1", Frontend: "TUI", Project: "/project"}, done: make(chan struct{})}
	s, raw, err := newSecureServer(ServeIdentity{Origin: "https://mac.example.ts.net", NodeID: "node", TailnetID: "tailnet"}, f, nil)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimPrefix(u.Fragment, "token=")
	t.Cleanup(func() { _ = s.Stop(context.Background()) })
	return s, f, token
}

func secureRequestTest(s *SecureServer, method, path, origin string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
	var data []byte
	if body != nil {
		data, _ = json.Marshal(body)
	}
	r := httptest.NewRequest(method, "https://mac.example.ts.net"+securePrefix+path, bytes.NewReader(data))
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func startTestPair(t *testing.T, s *SecureServer, token string) map[string]string {
	t.Helper()
	w := secureRequestTest(s, http.MethodPost, "pair/start", s.identity.Origin, map[string]string{"token": token, "device_name": "iPhone"}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("pair start: %d %s", w.Code, w.Body.String())
	}
	var pair map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &pair); err != nil {
		t.Fatal(err)
	}
	return pair
}

func TestSecurePairRequiresBothConfirmationsAndRevokes(t *testing.T) {
	s, _, token := newTestSecure(t)
	pair := startTestPair(t, s, token)
	req := <-s.PairRequests()
	if req.Code != pair["code"] || req.ID != pair["pair_id"] {
		t.Fatal("pair request mismatch")
	}
	body := map[string]string{"pair_id": pair["pair_id"], "proof": pair["proof"]}
	w := secureRequestTest(s, http.MethodPost, "pair/status", s.identity.Origin, body, nil)
	if w.Code != http.StatusOK || len(w.Result().Cookies()) != 0 || !strings.Contains(w.Body.String(), "waiting") {
		t.Fatal("issued credential before local confirmation")
	}
	if err := s.ConfirmPair(req.ID, "wrong", true); err == nil {
		t.Fatal("accepted wrong code")
	}
	if err := s.ConfirmPair(req.ID, req.Code, true); err != nil {
		t.Fatal(err)
	}
	w = secureRequestTest(s, http.MethodPost, "pair/status", s.identity.Origin, body, nil)
	cookies := w.Result().Cookies()
	if w.Code != http.StatusOK || len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie = %+v", cookies)
	}
	if w = secureRequestTest(s, http.MethodGet, "bootstrap", "", nil, cookies[0]); w.Code != http.StatusOK {
		t.Fatalf("bootstrap: %d", w.Code)
	}
	if w = secureRequestTest(s, http.MethodPost, "pair/status", s.identity.Origin, body, nil); w.Code != http.StatusUnauthorized {
		t.Fatal("replayed pairing")
	}
	if w = secureRequestTest(s, http.MethodPost, "pair/start", s.identity.Origin, map[string]string{"token": token}, nil); w.Code != http.StatusUnauthorized {
		t.Fatal("reused pairing token")
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if w = secureRequestTest(s, http.MethodGet, "bootstrap", "", nil, cookies[0]); w.Code != http.StatusGone {
		t.Fatal("credential survived stop")
	}
}

func TestSecurePairRejectsCrossOriginAndExpiry(t *testing.T) {
	s, _, token := newTestSecure(t)
	w := secureRequestTest(s, http.MethodPost, "pair/start", "https://evil.example", map[string]string{"token": token}, nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross origin: %d", w.Code)
	}
	s.mu.Lock()
	s.expires = time.Now().Add(-time.Second)
	s.mu.Unlock()
	w = secureRequestTest(s, http.MethodPost, "pair/start", s.identity.Origin, map[string]string{"token": token}, nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expired token: %d", w.Code)
	}
}

func TestSecurePairIsSingleUseUnderConcurrency(t *testing.T) {
	s, _, token := newTestSecure(t)
	var wg sync.WaitGroup
	results := make(chan int, 20)
	for range 20 {
		wg.Go(func() {
			w := secureRequestTest(s, http.MethodPost, "pair/start", s.identity.Origin, map[string]string{"token": token}, nil)
			results <- w.Code
		})
	}
	wg.Wait()
	close(results)
	accepted := 0
	for code := range results {
		if code == http.StatusOK {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted %d pairings", accepted)
	}
}

func TestSecureActionDeduplicatesAndBindsSession(t *testing.T) {
	s, f, _ := newTestSecure(t)
	s.paired = true
	frame := secureFrame{Version: 2, Type: "input.submit", InstanceID: s.instance, Epoch: s.epoch, SessionID: s.sessionID, RequestID: "0123456789abcdef0123456789abcdef", Text: "hello"}
	if got := s.handleSecureAction(context.Background(), frame); got != "accepted" {
		t.Fatal(got)
	}
	if got := s.handleSecureAction(context.Background(), frame); got != "accepted" {
		t.Fatal(got)
	}
	frame.Text = "different"
	if got := s.handleSecureAction(context.Background(), frame); got != "request_conflict" {
		t.Fatal(got)
	}
	frame.RequestID += "a"
	frame.SessionID = "other-session"
	if got := s.handleSecureAction(context.Background(), frame); got != "invalid_request" {
		t.Fatal(got)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.inputs) != 1 || f.inputs[0] != "hello" {
		t.Fatalf("inputs: %v", f.inputs)
	}
}

func TestSecureStopBeforeConnectionDoesNotCancelLocalApproval(t *testing.T) {
	s, f, _ := newTestSecure(t)
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.disconnects != 0 {
		t.Fatal("an unconnected remote server interrupted a local approval")
	}
}

func TestSecureRejectsControlCharactersAndChangedRoute(t *testing.T) {
	s, f, _ := newTestSecure(t)
	s.paired = true
	frame := secureFrame{Version: 2, Type: "input.submit", InstanceID: s.instance, Epoch: s.epoch, SessionID: s.sessionID, RequestID: "0123456789abcdef0123456789abcdef", Text: "\x1b]52;malicious"}
	if got := s.handleSecureAction(context.Background(), frame); got != "invalid_input" {
		t.Fatalf("control characters: %s", got)
	}
	frame.Text = "hello"
	s.verify = func(context.Context) (ServeIdentity, error) {
		return ServeIdentity{Origin: "https://other.example.ts.net", NodeID: "other", TailnetID: "tailnet"}, nil
	}
	if got := s.handleSecureAction(context.Background(), frame); got != "revoked" {
		t.Fatalf("changed route: %s", got)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.inputs) != 0 {
		t.Fatal("input was delivered after route validation failed")
	}
}

func TestSecureSnapshotIsBoundedAndMarksTruncation(t *testing.T) {
	s, f, _ := newTestSecure(t)
	f.mu.Lock()
	for range 200 {
		f.snapshot.Messages = append(f.snapshot.Messages, MobileMessage{Text: strings.Repeat("<", 4000)})
	}
	f.mu.Unlock()
	frame, err := s.snapshotFrame()
	if err != nil {
		t.Fatal(err)
	}
	if len(frame.Snapshot) > 120<<10 || !bytes.Contains(frame.Snapshot, []byte(`"truncated":true`)) {
		t.Fatalf("snapshot size=%d", len(frame.Snapshot))
	}
	other, err := s.snapshotFrame()
	if err != nil || other.Seq != frame.Seq {
		t.Fatal("unchanged snapshot got new sequence")
	}
}

func TestSecureSnapshotReplayAndExpiredHistory(t *testing.T) {
	s, f, _ := newTestSecure(t)
	first, err := s.snapshotFrame()
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"one", "two"} {
		f.mu.Lock()
		f.snapshot.Messages = append(f.snapshot.Messages, MobileMessage{Role: "assistant", Text: value})
		f.mu.Unlock()
		if _, err := s.snapshotFrame(); err != nil {
			t.Fatal(err)
		}
	}
	replay := s.snapshotsAfter(first.Seq, true)
	if len(replay) != 2 || replay[0].Seq != first.Seq+1 || replay[1].Seq != first.Seq+2 {
		t.Fatalf("replay: %+v", replay)
	}
	s.mu.Lock()
	for i := range s.history {
		s.history[i].at = time.Now().Add(-6 * time.Minute)
	}
	s.mu.Unlock()
	latest, err := s.snapshotFrame()
	if err != nil {
		t.Fatal(err)
	}
	resync := s.snapshotsAfter(first.Seq, true)
	if len(resync) != 1 || resync[0].Seq != latest.Seq {
		t.Fatalf("resync: %+v", resync)
	}
}

func TestMobileApprovalPreservesExactNeedsAndFailsClosed(t *testing.T) {
	req := judge.ApprovalRequest{Kind: judge.ApprovalConsent, Tool: "bash", Summary: "test", Needs: []judge.Need{{Cap: judge.CapWriteOutside, Path: "/tmp/out"}, {Cap: judge.CapUnconfined}}}
	view := ProjectApproval("id", req)
	if len(view.Choices) != 2 || len(view.Needs) != 2 || view.Needs[0].Path != "/tmp/out" {
		t.Fatalf("view: %+v", view)
	}
	answer, err := ApprovalAnswer(req, "once")
	if err != nil || !answer.Approve || answer.Exec != judge.ExecUnconfined || answer.Scope != judge.ScopeOnce || len(answer.Needs) != 2 {
		t.Fatalf("answer: %+v %v", answer, err)
	}
	req.Needs = append(req.Needs, judge.Need{Cap: judge.CapHostControl})
	if len(ProjectApproval("id", req).Choices) != 0 {
		t.Fatal("host control was exposed as a generic approval")
	}
	if _, err := ApprovalAnswer(req, "once"); err == nil {
		t.Fatal("unsupported approval accepted")
	}
}

func TestSecureWebSocketLocalProtocol(t *testing.T) {
	s, f, token := newTestSecure(t)
	pair := startTestPair(t, s, token)
	request := <-s.PairRequests()
	if err := s.ConfirmPair(request.ID, request.Code, true); err != nil {
		t.Fatal(err)
	}
	w := secureRequestTest(s, http.MethodPost, "pair/status", s.identity.Origin, map[string]string{"pair_id": pair["pair_id"], "proof": pair["proof"]}, nil)
	if len(w.Result().Cookies()) != 1 {
		t.Fatal("missing session cookie")
	}
	cookie := w.Result().Cookies()[0]
	// This loopback-only server simulates the trusted reverse-proxy hop. It does
	// not test Tailscale, a phone, or the TLS endpoint.
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Host = "mac.example.ts.net"
		s.ServeHTTP(w, r)
	}))
	defer proxy.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	headers := http.Header{"Origin": []string{s.identity.Origin}, "Cookie": []string{cookie.Name + "=" + cookie.Value}}
	endpoint := "ws" + strings.TrimPrefix(proxy.URL, "http") + securePrefix + "events"
	conn, _, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if err := wsjson.Write(ctx, conn, secureFrame{Version: 2, Type: "hello", InstanceID: s.instance, Epoch: s.epoch, CSRF: s.csrf}); err != nil {
		t.Fatal(err)
	}
	var snapshot secureFrame
	if err := wsjson.Read(ctx, conn, &snapshot); err != nil || snapshot.Type != "snapshot" || snapshot.SessionID != s.sessionID {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	other, response, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPHeader: headers})
	if other != nil {
		_ = other.CloseNow()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusConflict {
		t.Fatalf("second connection response=%v err=%v", response, err)
	}
	if response.Body != nil {
		_ = response.Body.Close()
	}
	input := secureFrame{Version: 2, Type: "input.submit", InstanceID: s.instance, Epoch: s.epoch, SessionID: s.sessionID, RequestID: "0123456789abcdef0123456789abcdef", Text: "hello"}
	if err := wsjson.Write(ctx, conn, input); err != nil {
		t.Fatal(err)
	}
	var result secureFrame
	if err := wsjson.Read(ctx, conn, &result); err != nil || result.Type != "result" || result.Result != "accepted" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.inputs) != 1 || f.inputs[0] != "hello" {
		t.Fatalf("inputs=%v", f.inputs)
	}
}
