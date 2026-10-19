package memstatus

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/cli/config"
	"github.com/getan/golder/internal/memory"
	"github.com/getan/golder/internal/runtime"
)

func TestRunMemoryRecall(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	var out bytes.Buffer

	// The shipped state: off, and the report says so.
	if !RunMemoryRecall(&out, "recall", nil) {
		t.Fatal("recall should be handled")
	}
	if !strings.Contains(out.String(), "off (default)") {
		t.Fatalf("show = %q", out.String())
	}
	if RunMemoryRecall(&out, "", nil) {
		t.Fatal("an empty argument is the report, not the recall subcommand")
	}

	out.Reset()
	store, err := memory.Open(filepath.Join(t.TempDir(), "m.db"), t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	reg := runtime.NewReminderRegistry(&runtime.MemoryReminderProvider{Store: store})

	if !RunMemoryRecall(&out, "recall on", reg) {
		t.Fatal("recall on should be handled")
	}
	if !strings.Contains(out.String(), "applies to this session") {
		t.Errorf("on = %q, want the live confirmation", out.String())
	}
	if !config.ResolveMemoryRecall().Recall {
		t.Fatal("the policy should be on after the command")
	}

	out.Reset()
	RunMemoryRecall(&out, "recall off", reg)
	if got := config.ResolveMemoryRecall(); got.Recall {
		t.Fatal("the policy should be off after the command")
	}
}
