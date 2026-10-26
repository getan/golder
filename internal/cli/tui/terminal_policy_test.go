package tui

import (
	"bytes"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
)

// Batch intentionally returns its only command directly when just one remains.
// SSH omits the background query, so tests must accept either shape.
func commandResults(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var results []tea.Msg
		for _, child := range batch {
			results = append(results, commandResults(child)...)
		}
		return results
	}
	return []tea.Msg{msg}
}

func TestAutomaticQueriesPolicyOnlyChangesSSHOrExplicitOptOut(t *testing.T) {
	for _, tc := range []struct{ name, key, value string }{
		{"local", "", ""},
		{"local reduced motion", "GOLDER_NO_ANIMATIONS", "1"},
		{"SSH tty", "SSH_TTY", "/dev/pts/test"},
		{"SSH connection", "SSH_CONNECTION", "fixture"},
		{"explicit opt out", "GOLDER_NO_TERMINAL_QUERIES", "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearMotionEnv(t)
			t.Setenv("GOLDER_NO_TERMINAL_QUERIES", "")
			if tc.key != "" {
				t.Setenv(tc.key, tc.value)
			}
			m := NewModel(Options{})
			wantDisabled := strings.HasPrefix(tc.name, "SSH") || tc.name == "explicit opt out"
			if m.noTerminalQueries != wantDisabled {
				t.Fatalf("queries disabled=%t, want %t", m.noTerminalQueries, wantDisabled)
			}
			foundBackground := false
			for _, msg := range commandResults(m.Init()) {
				if reflect.TypeOf(msg) == reflect.TypeOf(tea.RequestBackgroundColor()) {
					foundBackground = true
				}
			}
			if foundBackground == wantDisabled {
				t.Fatalf("background query present=%t, disabled=%t", foundBackground, wantDisabled)
			}
		})
	}
}

func TestQueryOutputEveryWriteBoundaryPreservesEverythingElse(t *testing.T) {
	const prefix = "hello\x1b[38;2;1;2;3m中文\x1b[0m\x1b[=1;1u\x1b[>4;2m"
	const suffix = "\x1b]52;c;?\x07\x1b[?25h\x1b[2;3Hworld"
	for q, query := range automaticTerminalQueries {
		for split := 0; split <= len(query); split++ {
			var sink bytes.Buffer
			w := &queryOutput{sink: &sink}
			for _, part := range [][]byte{[]byte(prefix), query[:split], query[split:], []byte(suffix)} {
				if n, err := w.Write(part); err != nil || n != len(part) {
					t.Fatalf("query %d split %d: write=%d,%v", q, split, n, err)
				}
			}
			if err := w.Flush(); err != nil {
				t.Fatal(err)
			}
			if got := sink.String(); got != prefix+suffix {
				t.Fatalf("query %d split %d changed modes/clipboard/cursor/text: %q", q, split, got)
			}
		}
	}
}

func TestQueryOutputPreservesFileIdentityAndDoesNotBypassFilter(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "tty-identity")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var sink bytes.Buffer
	w := &queryOutput{File: f, sink: &sink}
	if w.Fd() != f.Fd() || w.Name() != f.Name() {
		t.Fatal("query gate replaced the actual output file's tty identity")
	}
	if _, err := io.WriteString(w, "\x1b[?uA"); err != nil {
		t.Fatal(err)
	}
	// Hide strings.Reader's WriteTo to exercise the destination's ReadFrom.
	if _, err := io.Copy(w, struct{ io.Reader }{strings.NewReader("\x1b[?uB")}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("\x1b[?")); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := sink.String(); got != "AB\x1b[?" {
		t.Fatalf("promoted methods bypassed the gate or teardown lost bytes: %q", got)
	}
}

func TestSSHStatusBarUsesPlainASCIIDecorationsAndMatchingHitTest(t *testing.T) {
	clearMotionEnv(t)
	t.Setenv("SSH_TTY", "/dev/pts/test")
	s := NewModel(Options{ProviderName: "provider", Model: "model", ThinkingLevel: "high"}).statusBar
	s.cwd = "/project"
	s.SetColorProfile(colorprofile.TrueColor) // even a color-capable SSH terminal
	s.SetGit(gitInfoMsg{branch: "main", dirty: 2, ahead: 1, ok: true})
	s.SetMode("auto")
	s.SetTelemetry(telemetryEventView{tokens: 1200, window: 10000, util: .12})
	if !s.plain || !s.ascii {
		t.Fatal("SSH must not use filled powerline blocks")
	}
	for _, width := range []int{24, 80, 211} {
		plain := stripANSI(s.Render(width))
		for _, r := range plain {
			if r >= '\uE000' && r <= '\uF8FF' || strings.ContainsRune("⎇⚖✱⇄▸◔✽●⇡", r) {
				t.Fatalf("SSH status contains a decorative non-ASCII glyph: %q", plain)
			}
		}
		for x, cell := range s.row(width) {
			if got := s.segmentAt(x, width); got != segFocus(cell.seg) {
				t.Fatalf("width %d x=%d: hit=%d paint=%d", width, x, got, cell.seg)
			}
		}
	}
	if got := stripANSI(s.Render(211)); !strings.Contains(got, " | ") {
		t.Fatalf("SSH separator not ASCII: %q", got)
	}
}
