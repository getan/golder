package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/cli"
	"github.com/getan/golder/internal/diagtrace"
	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/session"
)

func TestInterruptAndLongTaskTraceIsContentFreeAndPreservesCancellation(t *testing.T) {
	diagtrace.Close()
	path := filepath.Join(t.TempDir(), "trace.log")
	if err := diagtrace.Init(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(diagtrace.Close)
	const secret = "trace-content-sentinel"
	m := apply(t, NewModel(Options{}), tea.WindowSizeMsg{Width: 80, Height: 24})
	m.running = true
	runCancelled, opCancelled := false, false
	m.interruptFn = func() { runCancelled = true }
	m.sessionOpCancel = func() { opCancelled = true }
	req := testApprovalRequest()
	req.Summary, req.Justification = secret, secret
	m.openApproval(req, make(chan judge.ApprovalAnswer, 1))
	m = apply(t, m, keyPress("ctrl+c"))
	if !runCancelled || !opCancelled || !m.interruptRequested {
		t.Fatal("tracing changed cancellation propagation")
	}
	if !m.approval.active {
		t.Fatal("an interrupt must not pretend the outstanding gate has settled")
	}
	m = apply(t, m, runEndMsg{err: context.Canceled})
	if m.approval.active || m.running {
		t.Fatal("the real run-end event must settle the dialog and run")
	}
	m.live.ContextWindow = 100000
	m.refreshContextUsage([]agentcore.Message{userMsg(secret)})
	now := time.Now()
	s := &runSession{
		store:    newTestStore(t),
		header:   session.SessionHeader{ID: "trace-session-sentinel", CreatedAt: now, UpdatedAt: now},
		live:     &cli.LiveConfig{Model: "fixture", ProviderName: "fixture"},
		agentCtx: &agentcore.AgentContext{Messages: []agentcore.Message{userMsg(secret)}},
	}
	if err := s.persist(); err != nil {
		t.Fatal(err)
	}
	diagtrace.Close()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		"interrupt received running=true approval=true selection=false draft=false session_op=true",
		"interrupt cancel requested", "run end received", "approval open", "approval close reason=run_end",
		"tui reflow begin", "tui reflow end duration=", "tui context_estimate end duration=",
		"session persist begin", "session persist end duration=",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing action/span %q:\n%s", want, text)
		}
	}
	for _, private := range []string{secret, "trace-session-sentinel", path} {
		if strings.Contains(text, private) {
			t.Fatalf("diagnostic exposed contents, identity or path: %q", private)
		}
	}
}
