package diagtrace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDisabledSpanDoesNotAllocate(t *testing.T) {
	Close()
	if got := testing.AllocsPerRun(100, func() { Span("test", "disabled")() }); got != 0 {
		t.Fatalf("disabled span allocated %v times", got)
	}
}

func TestSpanRecordsBeginAndDuration(t *testing.T) {
	Close()
	path := filepath.Join(t.TempDir(), "trace.log")
	if err := Init(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(Close)
	Span("test", "phase")()
	Close()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"test phase begin", "test phase end duration="} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing %q in span", want)
		}
	}
}
