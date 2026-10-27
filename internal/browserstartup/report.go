// Package browserstartup carries bounded startup diagnostics between a browser
// pane and its launcher. It records no post-CDP browser output or user input.
package browserstartup

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"unicode"

	"github.com/getan/golder/internal/privatepath"
	"github.com/getan/golder/internal/redact"
)

const Limit = 16 << 10

type Report struct {
	Phase    string `json:"phase"`
	State    string `json:"state"`
	Kind     string `json:"kind,omitempty"`
	Error    string `json:"error,omitempty"`
	Output   string `json:"output,omitempty"`
	ExitCode int    `json:"exit_code,omitempty"`
}

type Reporter struct {
	mu      sync.Mutex
	path    string
	report  Report
	buf     []byte
	stopped bool
	r       *redact.Redactor
}

func New(path string) (*Reporter, error) {
	if path != "" {
		if !filepath.IsAbs(path) {
			return nil, fmt.Errorf("browser startup report path must be absolute")
		}
		if err := privatepath.EnsureDir(filepath.Dir(path)); err != nil {
			return nil, err
		}
	}
	r := redact.Default()
	if r == nil {
		var err error
		r, err = redact.New(redact.Config{Mode: redact.Redact})
		if err != nil {
			return nil, err
		}
	}
	s := &Reporter{path: path, r: r, report: Report{State: "starting", Phase: "starting"}}
	if err := s.save(); err != nil {
		return nil, err
	}
	return s, nil
}

// Write retains only the FIRST bounded portion of startup stderr, never a
// growing log. Truncation happens before persistence, and incomplete trailing
// lines are discarded so a cut secret is not written without its rule prefix.
func (s *Reporter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(p)
	if !s.stopped && len(s.buf) < Limit {
		s.buf = append(s.buf, p[:min(len(p), Limit-len(s.buf))]...)
	}
	return n, nil
}

func (s *Reporter) Phase(phase string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.report.State != "starting" {
		return
	}
	s.report.Phase = phase
	if phase == "browser_connected" || phase == "ready" {
		s.stopped = true
		clear(s.buf)
		s.buf = nil
	}
	if phase == "ready" {
		s.report.State = "ready"
	}
	_ = s.save()
}

func (s *Reporter) Fail(err error, code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil || s.report.State == "ready" {
		return
	}
	s.stopped = true
	s.report.State, s.report.ExitCode = "failed", code
	s.report.Error = Sanitize(s.r, err.Error())
	text := string(s.buf)
	if len(s.buf) == Limit {
		if end := strings.LastIndexByte(text, '\n'); end >= 0 {
			text = text[:end]
		} else {
			text = "[oversized startup diagnostic omitted]"
		}
	}
	s.report.Output = Sanitize(s.r, text)
	s.report.Kind = FailureKind(s.report.Phase, s.report.Error+"\n"+s.report.Output)
	_ = s.save()
	clear(s.buf)
	s.buf = nil
}

func FailureKind(phase, text string) string {
	t := strings.ToLower(text)
	switch {
	case strings.Contains(t, "executable file not found"), strings.Contains(t, "fork/exec ") && strings.Contains(t, "no such file or directory"):
		return "browser_not_installed"
	case strings.Contains(t, "operation not permitted"), strings.Contains(t, "permission denied"), strings.Contains(t, "sandbox initialization failed"), strings.Contains(t, "bootstrap_check_in"):
		return "runtime_permission"
	case phase == "initializing_terminal" || phase == "initializing_renderer" || phase == "browser_connected":
		return "terminal_initialization"
	case strings.Contains(t, "signal"), strings.Contains(t, "fatal:"), strings.Contains(t, "chrome failed to start"):
		return "browser_crash"
	default:
		return "startup_failed"
	}
}

func Sanitize(r *redact.Redactor, text string) string {
	// Keep complete text through redaction before bounding the displayed error.
	text = r.Text(redact.SeamToolOutput, "browser_startup", text)
	text = startupURLs.ReplaceAllStringFunc(text, func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil {
			return "[startup URL omitted]"
		}
		u.User = nil
		u.RawQuery = ""
		u.Fragment = ""
		return u.String()
	})
	text = strings.Map(func(c rune) rune {
		if unicode.IsControl(c) && c != '\n' && c != '\t' {
			return -1
		}
		return c
	}, text)
	if len(text) > Limit {
		text = text[:Limit] + "\n[startup diagnostic truncated]"
	}
	return text
}

var startupURLs = regexp.MustCompile(`(?i)(?:https?|wss?)://[^\s"'<>]+`)

func (s *Reporter) ErrorText(err error) string { return Sanitize(s.r, err.Error()) }

func (s *Reporter) save() error {
	if s.path == "" {
		return nil
	}
	raw, err := json.Marshal(s.report)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".browser-startup-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(raw); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(name, s.path)
}

func Read(path string) (Report, error) {
	var r Report
	// JSON escaping may expand the two bounded diagnostic strings sixfold.
	raw, err := privatepath.ReadFile(path, 0o600, 16*Limit)
	if err != nil {
		return r, err
	}
	err = json.Unmarshal(raw, &r)
	return r, err
}

// Message is untrusted, bounded, redacted diagnostic text, never instructions.
func (r Report) Message() string {
	return fmt.Sprintf("browser startup failed (%s, phase=%s, exit=%d): %s\n%s", r.Kind, r.Phase, r.ExitCode, r.Error, r.Output)
}

func (s *Reporter) Snapshot() Report {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.report
}
