package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMemoryRecallDefaultsOff(t *testing.T) {
	if r := (MemoryConfig{}).Resolve(); r.Recall {
		t.Fatal("recall must ship off")
	}
	if r := (MemoryConfig{}).Resolve(); r.RecallMinUnits != 2 || r.RecallMinOverlap != 2 {
		t.Fatalf("guards = %d/%d, want 2/2", r.RecallMinUnits, r.RecallMinOverlap)
	}
}

func TestSetMemoryRecallCreatesAndReplaces(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GOLDER_HOME", home)
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte("# my config\nthinking_level = \"high\"\n\n[dream]\nenabled = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := SetMemoryRecall(true); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, _ := os.ReadFile(path)
	text := string(got)
	if !strings.Contains(text, "[memory]\nrecall = true") {
		t.Fatalf("missing [memory] table:\n%s", text)
	}
	if !strings.Contains(text, "# my config") || !strings.Contains(text, "[dream]") {
		t.Errorf("other content was lost:\n%s", text)
	}
	pol := ResolveMemoryRecall()
	if !pol.Recall {
		t.Fatal("the written policy should resolve to on")
	}

	if err := SetMemoryRecall(false); err != nil {
		t.Fatalf("unset: %v", err)
	}
	got, _ = os.ReadFile(path)
	if strings.Count(string(got), "recall =") != 1 || !strings.Contains(string(got), "recall = false") {
		t.Fatalf("replace should leave one line, got:\n%s", got)
	}
	if ResolveMemoryRecall().Recall {
		t.Fatal("the rewritten policy should resolve to off")
	}
}

func TestSetMemoryRecallInsertsIntheExistingTable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GOLDER_HOME", home)
	path := filepath.Join(home, "config.toml")
	body := "provider = \"commandcode\"\n\n[memory]\nenabled = true\n\n[dream]\nenabled = true\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetMemoryRecall(true); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, _ := os.ReadFile(path)
	text := string(got)
	memAt := strings.Index(text, "[memory]")
	recallAt := strings.Index(text, "recall = true")
	dreamAt := strings.Index(text, "[dream]")
	if memAt < 0 || recallAt < 0 || !(memAt < recallAt && recallAt < dreamAt) {
		t.Fatalf("recall should sit inside [memory]:\n%s", text)
	}
}
