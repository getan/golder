package remotecontrol

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

type secureFrame struct {
	Version    int             `json:"v"`
	Type       string          `json:"type"`
	InstanceID string          `json:"instance_id,omitempty"`
	Epoch      string          `json:"epoch,omitempty"`
	SessionID  string          `json:"session_id,omitempty"`
	Seq        uint64          `json:"seq,omitempty"`
	RequestID  string          `json:"request_id,omitempty"`
	CSRF       string          `json:"csrf,omitempty"`
	Text       string          `json:"text,omitempty"`
	ApprovalID string          `json:"approval_id,omitempty"`
	ChoiceID   string          `json:"choice_id,omitempty"`
	Result     string          `json:"result,omitempty"`
	Snapshot   json.RawMessage `json:"snapshot,omitempty"`
}

func (s *SecureServer) handleSecureWS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || !s.authenticated(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	s.mu.Lock()
	if s.stopped || s.reserved || s.client != nil {
		s.mu.Unlock()
		http.Error(w, "controller busy", http.StatusConflict)
		return
	}
	s.reserved = true
	s.mu.Unlock()
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		s.mu.Lock()
		s.reserved = false
		s.mu.Unlock()
		return
	}
	conn.SetReadLimit(secureMaxFrame)
	s.mu.Lock()
	if s.stopped {
		s.reserved = false
		s.mu.Unlock()
		_ = conn.Close(websocket.StatusGoingAway, "stopped")
		return
	}
	s.client = conn
	s.reserved = false
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.client == conn {
			s.client = nil
			s.reserved = true
		}
		s.mu.Unlock()
		s.session.Disconnected()
		s.mu.Lock()
		s.reserved = false
		s.mu.Unlock()
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	authCtx, authCancel := context.WithTimeout(ctx, 5*time.Second)
	var hello secureFrame
	err = wsjson.Read(authCtx, conn, &hello)
	authCancel()
	if err != nil || hello.Version != 2 || hello.Type != "hello" || hello.InstanceID != s.instance || hello.Epoch != s.epoch || !s.validCSRF(hello.CSRF) {
		_ = conn.Close(websocket.StatusPolicyViolation, "authentication failed")
		return
	}

	var writeMu sync.Mutex
	write := func(frame secureFrame) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		writeCtx, done := context.WithTimeout(ctx, 5*time.Second)
		defer done()
		return wsjson.Write(writeCtx, conn, frame)
	}
	var pump sync.WaitGroup
	pump.Add(2)
	go func() {
		defer pump.Done()
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pingCtx, done := context.WithTimeout(ctx, 8*time.Second)
				err := conn.Ping(pingCtx)
				done()
				if err != nil {
					_ = conn.CloseNow()
					return
				}
			}
		}
	}()
	go func() {
		defer pump.Done()
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		sent := hello.Seq
		first := true
		for {
			_, err := s.snapshotFrame()
			if err != nil {
				_ = conn.Close(websocket.StatusInternalError, "session unavailable")
				return
			}
			for _, frame := range s.snapshotsAfter(sent, first) {
				if err := write(frame); err != nil {
					return
				}
				sent = frame.Seq
			}
			first = false
			select {
			case <-ctx.Done():
				return
			case <-s.stop:
				return
			case <-ticker.C:
			}
		}
	}()
	defer func() {
		cancel()
		pump.Wait()
	}()

	for {
		var frame secureFrame
		if err := wsjson.Read(ctx, conn, &frame); err != nil {
			return
		}
		result := s.handleSecureAction(ctx, frame)
		if err := write(secureFrame{Version: 2, Type: "result", InstanceID: s.instance, Epoch: s.epoch, SessionID: s.sessionID, RequestID: frame.RequestID, Result: result}); err != nil {
			return
		}
	}
}

// snapshotFrame serializes capture and publication, so an older capture cannot
// be assigned a newer sequence when two callers race. Each event is a complete
// snapshot; bounded history can be replayed or replaced by the latest snapshot.
func (s *SecureServer) snapshotFrame() (secureFrame, error) {
	s.captureMu.Lock()
	defer s.captureMu.Unlock()
	snapshot, err := s.session.Snapshot()
	if err != nil || snapshot.SessionID != s.sessionID {
		return secureFrame{}, errors.New("session changed")
	}
	// Bound each text item, then remove oldest transcript items until the whole
	// snapshot fits. Approval details are never silently truncated.
	for i := range snapshot.Messages {
		if len(snapshot.Messages[i].Text) > 64<<10 {
			text := snapshot.Messages[i].Text[len(snapshot.Messages[i].Text)-(64<<10):]
			for !utf8.ValidString(text) && len(text) > 0 {
				text = text[1:]
			}
			snapshot.Messages[i].Text = "[earlier text omitted]\n" + text
			snapshot.Truncated = true
		}
	}
	messages := snapshot.Messages
	snapshot.Messages = nil
	base, err := json.Marshal(snapshot)
	if err != nil {
		return secureFrame{}, err
	}
	budget := (120 << 10) - len(base) - 256
	if budget < 0 {
		return secureFrame{}, errors.New("approval details exceed snapshot limit")
	}
	start := len(messages)
	for i := len(messages) - 1; i >= 0; i-- {
		encoded, marshalErr := json.Marshal(messages[i])
		if marshalErr != nil {
			return secureFrame{}, marshalErr
		}
		if len(encoded)+1 > budget {
			break
		}
		budget -= len(encoded) + 1
		start = i
	}
	if start > 0 {
		snapshot.Truncated = true
	}
	snapshot.Messages = messages[start:]
	data, err := json.Marshal(snapshot)
	if err != nil || len(data) > 120<<10 {
		return secureFrame{}, errors.New("snapshot exceeds limit")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return secureFrame{}, errors.New("stopped")
	}
	now := time.Now()
	if !bytes.Equal(data, s.lastSnapshot) {
		s.seq++
		s.lastSnapshot = append(s.lastSnapshot[:0], data...)
		entry := secureSnapshotEvent{seq: s.seq, data: append([]byte(nil), data...), at: now}
		s.history = append(s.history, entry)
		s.historyBytes += len(entry.data)
	}
	for len(s.history) > 0 && (s.historyBytes > 1<<20 || now.Sub(s.history[0].at) > 5*time.Minute) {
		s.historyBytes -= len(s.history[0].data)
		s.history[0] = secureSnapshotEvent{}
		s.history = s.history[1:]
	}
	return secureFrame{Version: 2, Type: "snapshot", InstanceID: s.instance, Epoch: s.epoch, SessionID: s.sessionID, Seq: s.seq, Snapshot: append([]byte(nil), data...)}, nil
}

func (s *SecureServer) captureSnapshots() {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			s.mu.Lock()
			paired := s.paired && !s.stopped
			s.mu.Unlock()
			if paired {
				if _, err := s.snapshotFrame(); err != nil {
					_ = s.Stop(context.Background())
					return
				}
			}
		}
	}
}

func (s *SecureServer) snapshotsAfter(after uint64, first bool) []secureFrame {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.seq == 0 {
		return nil
	}
	frame := func(seq uint64, data []byte) secureFrame {
		return secureFrame{Version: 2, Type: "snapshot", InstanceID: s.instance, Epoch: s.epoch, SessionID: s.sessionID, Seq: seq, Snapshot: append([]byte(nil), data...)}
	}
	if after == 0 || after > s.seq || len(s.history) == 0 || after+1 < s.history[0].seq {
		return []secureFrame{frame(s.seq, s.lastSnapshot)}
	}
	var out []secureFrame
	for _, event := range s.history {
		if event.seq > after {
			out = append(out, frame(event.seq, event.data))
		}
	}
	if first && len(out) == 0 {
		out = append(out, frame(s.seq, s.lastSnapshot))
	}
	return out
}

func (s *SecureServer) handleSecureAction(ctx context.Context, frame secureFrame) string {
	if frame.Version != 2 || frame.InstanceID != s.instance || frame.Epoch != s.epoch || frame.SessionID != s.sessionID || len(frame.RequestID) < 16 || len(frame.RequestID) > 128 {
		return "invalid_request"
	}
	if frame.Type != "input.submit" && frame.Type != "approval.answer" {
		return "invalid_request"
	}
	if frame.Type == "input.submit" && (len(frame.Text) == 0 || len(frame.Text) > secureMaxInput || !utf8.ValidString(frame.Text) || strings.TrimSpace(frame.Text) == "" || strings.IndexFunc(frame.Text, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }) >= 0) {
		return "invalid_input"
	}
	if frame.Type == "approval.answer" && (frame.ApprovalID == "" || frame.ChoiceID == "") {
		return "invalid_request"
	}
	frame.CSRF = ""
	raw, err := json.Marshal(frame)
	if err != nil {
		return "invalid_request"
	}
	digest := sha256.Sum256(raw)
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	if s.stopped || !s.paired {
		s.mu.Unlock()
		return "revoked"
	}
	if previous, ok := s.requests[frame.RequestID]; ok {
		s.mu.Unlock()
		if previous.digest != digest {
			return "request_conflict"
		}
		return previous.result
	}
	if len(s.requests) >= secureMaxRequests {
		s.mu.Unlock()
		return "request_limit"
	}
	// Reserve before calling the adapter. A cancellation or uncertain outcome is
	// never retried as a new operation in this instance.
	s.requests[frame.RequestID] = secureRequest{digest: digest, result: "outcome_unknown"}
	s.mu.Unlock()
	if s.verify != nil {
		verifyCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		identity, verifyErr := s.verify(verifyCtx)
		cancel()
		if verifyErr != nil || identity != s.identity {
			s.Revoke()
			go func() { _ = s.Stop(context.Background()) }()
			return "revoked"
		}
	}
	s.mu.Lock()
	active := !s.stopped && s.paired
	s.mu.Unlock()
	if !active {
		return "revoked"
	}
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if frame.Type == "input.submit" {
		err = s.session.Submit(callCtx, frame.Text)
	} else {
		err = s.session.Answer(callCtx, frame.ApprovalID, frame.ChoiceID)
	}
	result := "accepted"
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			result = "outcome_unknown"
		} else {
			result = "rejected"
		}
	}
	s.mu.Lock()
	s.requests[frame.RequestID] = secureRequest{digest: digest, result: result}
	s.mu.Unlock()
	return result
}
