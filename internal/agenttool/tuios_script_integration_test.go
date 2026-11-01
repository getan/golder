package agenttool

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/tuiosctl"
	"github.com/kballard/go-shellquote"
)

type tuiosReceiptStream struct {
	mu      sync.Mutex
	text    string
	changed chan struct{}
}

func (s *tuiosReceiptStream) Write(b []byte) (int, error) {
	s.mu.Lock()
	s.text += string(b)
	close(s.changed)
	s.changed = make(chan struct{})
	s.mu.Unlock()
	return len(b), nil
}
func (s *tuiosReceiptStream) snapshot() (string, <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.text, s.changed
}

// Real POSIX child + the production facade/client + socket protocol fixture.
// The fixture only forwards bytes and subscribes to output: it does not invent
// READY/DONE receipts. Loss of DONE must not cause a second file modification.
func TestTuiosScriptFacadeReceipts(t *testing.T) {
	for _, lostDone := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "lost-completion"}[lostDone], func(t *testing.T) {
			f, sock := startTuiosFake(t)
			useTuiosFake(t, sock)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			stream := &tuiosReceiptStream{changed: make(chan struct{})}
			var input io.WriteCloser
			var done chan error
			f.answer = func(verb string, p map[string]any) any {
				switch verb {
				case "send-text":
					text := p["text"].(string)
					if input == nil {
						argv, err := shellquote.Split(text)
						if err != nil {
							t.Error(err)
							return &tuiosctl.Error{Code: "fixture", Message: err.Error()}
						}
						cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
						cmd.Stdout = stream
						cmd.Stderr = stream
						input, err = cmd.StdinPipe()
						if err != nil {
							return &tuiosctl.Error{Code: "fixture", Message: err.Error()}
						}
						if err = cmd.Start(); err != nil {
							return &tuiosctl.Error{Code: "fixture", Message: err.Error()}
						}
						done = make(chan error, 1)
						go func() { done <- cmd.Wait() }()
					} else if _, err := io.WriteString(input, text); err != nil {
						return &tuiosctl.Error{Code: "fixture", Message: err.Error()}
					}
					return map[string]any{"type": "ok"}
				case "wait-for":
					if p["condition"] == "window-exit" {
						return &tuiosctl.Error{Code: "timeout", Message: "still open"}
					}
					re := regexp.MustCompile(p["pattern"].(string))
					for {
						text, changed := stream.snapshot()
						if re.MatchString(text) {
							if lostDone && !strings.Contains(p["pattern"].(string), "(R|") {
								return &tuiosctl.Error{Code: "timeout", Message: "lost completion receipt"}
							}
							return map[string]any{"type": "wait_result", "matched": true, "window": "w2"}
						}
						select {
						case <-changed:
						case <-ctx.Done():
							return &tuiosctl.Error{Code: "timeout", Message: "fixture deadline"}
						}
					}
				case "capture-pane":
					text, _ := stream.snapshot()
					return map[string]any{"type": "pane_content", "content": text}
				}
				return map[string]any{"type": "ok"}
			}
			path := filepath.Join(t.TempDir(), "changes")
			script := "printf 'once\\n' >> " + quoteControlArg(path) + "\n#" + strings.Repeat("large-content", 10000)
			raw, _ := json.Marshal(map[string]any{"action": "run", "window": "w2", "script": script})
			res, err := newTuiosTool(t, true).Execute(ctx, "call-server-two", raw, nil)
			if err != nil {
				t.Fatal(err)
			}
			if res.IsError != lostDone {
				t.Fatalf("result=%s", agentcore.ContentToText(res.Content))
			}
			if done == nil {
				t.Fatal("receiver was not started")
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			_ = input.Close()
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "once\n" {
				t.Fatalf("script was lost or repeated: %q %v", data, err)
			}
			text := agentcore.ContentToText(res.Content)
			if !strings.Contains(text, "call-server-two") || !strings.Contains(text, "w2") {
				t.Fatal("routing absent from model-visible body")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			sends, captures := 0, 0
			for _, v := range f.verbs {
				if v == "send-text" {
					sends++
				}
				if v == "capture-pane" {
					captures++
				}
			}
			if sends != 2 || captures > 2 || f.seenUnsafeRun() {
				t.Fatalf("unexpected polling/retry/per-chunk round trips: sends=%d captures=%d", sends, captures)
			}
		})
	}
}

func (f *tuiosFake) seenUnsafeRun() bool {
	for _, v := range f.verbs {
		if v == "run" {
			return true
		}
	}
	return false
}
