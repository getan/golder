package extensions

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReloadSourcesSeesOneAnotherProcessAdded(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GOLDER_HOME", home)
	path := filepath.Join(home, "config.toml")
	early, err := New(Options{ConfigPath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer early.Close()
	late, err := New(Options{ConfigPath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer late.Close()
	ref := SourceRef{Kind: "mcp", ID: "docx"}
	exe := filepath.Join(home, "golder-docx")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := late.ReplaceSourceConfig(ref, SourceConfig{Transport: "stdio", Command: exe, Args: []string{"serve"}}); err != nil {
		t.Fatal(err)
	}
	reviewed, err := late.PrepareMCPEdit(ref.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := late.EnableReviewedMCP(reviewed, true); err != nil {
		t.Fatal(err)
	}
	if _, ok := early.Source(ref); ok {
		t.Fatal("the early manager saw a source before reloading")
	}
	if err := early.ReloadSources(); err != nil {
		t.Fatal(err)
	}
	snap, ok := early.Source(ref)
	if !ok || !snap.Enabled || snap.Trust != "approved" {
		t.Fatalf("reloaded source = %+v, %v", snap, ok)
	}
}
