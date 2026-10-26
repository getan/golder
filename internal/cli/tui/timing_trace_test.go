package tui

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/getan/golder/internal/diagtrace"
)

type timingTestModel struct{ updates int }

func (timingTestModel) Init() tea.Cmd { return func() tea.Msg { return "init command" } }
func (m timingTestModel) Update(tea.Msg) (tea.Model, tea.Cmd) {
	m.updates++
	return m, func() tea.Msg { return "update command" }
}
func (timingTestModel) View() tea.View { return tea.NewView("private assistant output") }

func TestTimingTraceDisabled(t *testing.T) {
	trace := newTimingTrace(nil)
	base := timingTestModel{}
	m, opts := trace.program(base)
	if m != base || len(opts) != 0 {
		t.Fatal("disabled tracing must not wrap the model or change program options")
	}
	trace.event("noop")
	trace.close()
}

func TestTimingTraceDoesNotRecordContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.log")
	t.Setenv("TERM", "xterm")
	t.Setenv("COLORTERM", "")
	t.Setenv("SSH_CONNECTION", "private remote address")
	t.Setenv("GOLDER_TRACE_TEST_CREDENTIAL", "private credential")
	raw, err := diagtrace.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	trace := newTimingTrace(raw)
	m := timingModel{Model: timingTestModel{}, trace: trace}
	if cmd := m.Init(); cmd == nil || cmd() != "init command" {
		t.Fatal("tracing must preserve Init commands")
	}
	messages := []tea.Msg{
		tea.KeyPressMsg{Code: 'a', Text: "private keystrokes"},
		tea.PasteMsg{Content: "private pasted prompt"},
		tea.EnvMsg{"GOLDER_TRACE_TEST_CREDENTIAL=private credential"},
		tea.WindowSizeMsg{Width: 120, Height: 40},
		tea.ColorProfileMsg{Profile: colorprofile.ANSI},
		tea.KeyboardEnhancementsMsg{Flags: 1},
	}
	for i, msg := range messages {
		next, cmd := m.Update(msg)
		m = next.(timingModel)
		if m.Model.(timingTestModel).updates != i+1 {
			t.Fatal("tracing must keep the updated model inside the wrapper")
		}
		if cmd == nil || cmd() != "update command" {
			t.Fatal("tracing must preserve Update commands")
		}
	}
	if got := m.View().Content; got != "private assistant output" {
		t.Fatalf("tracing changed View: %q", got)
	}
	trace.close()
	raw.Close()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		`env TERM="xterm"`, "SSH_CONNECTION_set=true", "window width=120 height=40",
		"color profile=ANSI", "keyboard flags=1", "type=tea.KeyPressMsg", "type=tea.PasteMsg",
		"view end bytes=", "duration=", "trace end",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("trace missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "private") || strings.Contains(text, "GOLDER_TRACE_TEST_CREDENTIAL") {
		t.Fatalf("trace leaked user content or environment credentials:\n%s", text)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Errorf("trace should be created private: %v, %v", info, err)
	}
}

func TestTimingTraceFileIO(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trace.log")
	raw, err := diagtrace.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	trace := newTimingTrace(raw)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	input := timingInput{File: r, trace: trace}
	if input.Fd() != r.Fd() || input.Name() != r.Name() {
		t.Fatal("input wrapper must preserve terminal file identity")
	}
	if _, err := w.Write([]byte("private input")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	n, err := input.Read(buf)
	if err != nil || string(buf[:n]) != "private input" {
		t.Fatalf("read = %q, %v", buf[:n], err)
	}
	outPath := filepath.Join(dir, "output")
	f, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	output := timingOutput{File: f, trace: trace}
	if output.Fd() != f.Fd() || output.Name() != f.Name() {
		t.Fatal("output wrapper must preserve terminal file identity")
	}
	// Bubble Tea's renderer uses io.Copy from a bytes.Buffer. Test that path,
	// not just Write directly, so a promoted method cannot bypass the trace.
	if _, err := io.Copy(output, bytes.NewBufferString("private output")); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	trace.close()
	raw.Close()
	out, _ := os.ReadFile(outPath)
	if string(out) != "private output" {
		t.Fatalf("tracing changed output: %q", out)
	}
	data, _ := os.ReadFile(path)
	for _, want := range []string{"input read bytes=13", "output begin bytes=14", "output end bytes=14"} {
		if !bytes.Contains(data, []byte(want)) {
			t.Errorf("missing %q in %s", want, data)
		}
	}
	if bytes.Contains(data, []byte("private")) {
		t.Fatalf("I/O trace leaked terminal content: %s", data)
	}
}

func TestTimingTraceBadPath(t *testing.T) {
	trace, err := diagtrace.Open(filepath.Join(t.TempDir(), "missing", "trace.log"))
	if trace != nil || err == nil {
		t.Fatalf("bad path trace = %v, %v", trace, err)
	}
}
