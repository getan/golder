package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"

	"github.com/getan/golder/internal/diagtrace"
)

func TestWorkspaceInputTraceContainsOnlyMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace")
	trace, err := diagtrace.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(trace.Close)
	restore := observeWorkspaceInput(trace)
	defer restore()
	const privateText = "private-keystrokes-and-paste"
	reader := uv.NewTerminalReader(strings.NewReader("\x1b[200~"+privateText+"\x1b[201~"), "xterm")
	events := make(chan uv.Event, 64)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := reader.StreamEvents(ctx, events); err != nil {
		t.Fatal(err)
	}
	traceWorkspaceStacks(trace)
	trace.Close()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{"workspace.input reader.init.begin", "workspace.input stream.begin", "workspace.input decode.end", "workspace.input stream.end", "workspace.stacks sample begin", "workspace.stacks functions=", "workspace.stacks sample end"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing timing phase %q", want)
		}
	}
	if strings.Contains(text, privateText) {
		t.Fatal("trace leaked input contents")
	}
	if strings.Contains(text, path) || strings.Contains(text, "input_trace_test.go") {
		t.Fatal("stack sample leaked source or diagnostic paths")
	}
	if len(events) == 0 {
		t.Fatal("observer prevented input delivery")
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("metadata observation stalled input: %v", err)
	}
}
