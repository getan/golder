package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/getan/golder/internal/diagtrace"
)

func TestDiagnosticCompletionAndExitContainNoDraft(t *testing.T) {
	diagtrace.Close()
	path := filepath.Join(t.TempDir(), "trace.log")
	if err := diagtrace.Init(path); err != nil {
		t.Fatal(err)
	}
	defer diagtrace.Close()
	m := timingModel{Model: NewModel(Options{NoAnimations: true, NoPromptTemplates: true}), trace: newTimingTrace(diagtrace.Process())}
	for _, msg := range []tea.Msg{
		tea.WindowSizeMsg{Width: 80, Height: 24},
		tea.KeyPressMsg{Code: '/', Text: "/"},
		tea.KeyPressMsg{Code: 'p', Text: "p"},
		tea.KeyPressMsg{Code: 'r', Text: "r"},
		tea.KeyPressMsg{Code: 'o', Text: "o"},
		tea.KeyPressMsg{Code: tea.KeyTab},
	} {
		next, _ := m.Update(msg)
		m = next.(timingModel)
	}
	base := m.Model.(Model)
	if base.quitting || base.input.Value() != "/provider " {
		t.Fatal("diagnostics changed Tab completion")
	}
	base.input.SetValue("PRIVATE_DRAFT_TEXT")
	m.Model = base
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if !next.(timingModel).Model.(Model).quitting || cmd == nil {
		t.Fatal("diagnostics changed Ctrl+D behavior")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"control key=tab", "completion begin", "completion apply", "completion end", "control key=ctrl_d", "exit intent source=ctrl_d accepted=true"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %s", want)
		}
	}
	for _, secret := range []string{"PRIVATE_DRAFT_TEXT", "/pro", "/provider"} {
		if strings.Contains(text, secret) {
			t.Errorf("trace leaked draft/command: %s", secret)
		}
	}
}

type diagnosticPanicModel struct{ timingTestModel }

func (diagnosticPanicModel) Update(tea.Msg) (tea.Model, tea.Cmd) {
	panic("PRIVATE_UPDATE_PANIC")
}

func TestDiagnosticPanicAndInputEOF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.log")
	raw, err := diagtrace.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	trace := newTimingTrace(raw)
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		m := timingModel{Model: diagnosticPanicModel{}, trace: trace}
		_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	}()
	if recovered != "PRIVATE_UPDATE_PANIC" {
		t.Fatal("traced Update did not rethrow the same panic")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	_ = w.Close()
	_, _ = (timingInput{File: r, trace: trace}).Read(make([]byte, 1))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "panic phase=tui.update kind=string") || !strings.Contains(text, "error_class=eof") || strings.Contains(text, "PRIVATE_") {
		t.Fatal("missing safe panic/EOF evidence or leaked private values")
	}
}

func TestDiagnosticControlClassification(t *testing.T) {
	for _, tc := range []struct {
		key  tea.KeyPressMsg
		want string
	}{
		{tea.KeyPressMsg{Code: tea.KeyTab}, "tab"},
		{tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}, "tab"},
		{tea.KeyPressMsg{Code: tea.KeyEnter}, "enter"},
		{tea.KeyPressMsg{Code: tea.KeyEscape}, "escape"},
		{tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}, "ctrl_c"},
		{tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}, "ctrl_d"},
		{tea.KeyPressMsg{Code: 'x', Text: "PRIVATE_INPUT"}, ""},
	} {
		if got := traceControlKey(tc.key); got != tc.want {
			t.Errorf("control class=%s want=%s", got, tc.want)
		}
	}
	for _, tc := range []struct {
		err  error
		want string
	}{
		{nil, "none"}, {tea.ErrProgramPanic, "panic"}, {tea.ErrInterrupted, "interrupted"},
		{tea.ErrProgramKilled, "program_killed"},
		{errors.Join(tea.ErrProgramKilled, syscall.EPIPE), "broken_pipe"},
	} {
		if got := programErrorClass(tc.err); got != tc.want {
			t.Errorf("program class=%s want=%s", got, tc.want)
		}
	}
}
