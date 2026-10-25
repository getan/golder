package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskFreshPreservesCustomSystemPrompt(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GOLDER_HOME", t.TempDir())
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("Project rule: run the unit tests."), 0o644); err != nil {
		t.Fatal(err)
	}
	// A custom base can itself contain the old text delimiter. None of these
	// user instructions may disappear when preparing a fresh child's context.
	const base = "Use only staging, never production.\n\nEnvironment:\nCustom instruction after the marker."
	parent, err := (PromptInputs{Base: base, Appends: []string{"Append rule: do not push."}, WorkingDir: dir}).Build()
	if err != nil {
		t.Fatal(err)
	}
	child := taskChildSystemPrompt(context.Background(), taskSystemPrompt, parent, "", SubAgentContextFresh)
	if !strings.HasPrefix(child, parent) {
		t.Fatalf("fresh child lost parent system instructions:\n%s", child)
	}
	for _, text := range []string{base, "Project rule: run the unit tests.", "Append rule: do not push.", "cannot spawn further sub-agents", "no parent conversation was inherited"} {
		if !strings.Contains(child, text) {
			t.Errorf("fresh child prompt is missing %q", text)
		}
	}
}
