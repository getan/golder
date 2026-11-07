package appsession

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/getan/golder/internal/extensions"
)

func TestAddMCPSavesDisabledWithoutConnecting(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GOLDER_HOME", home)
	manager, err := extensions.New(extensions.Options{ConfigPath: filepath.Join(home, "config.toml"), Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	c := New(Options{Extensions: manager})
	draft := extensions.MCPDraft{ID: "demo", Transport: "http", URL: "https://example.invalid/mcp"}
	if err := c.AddMCP(context.Background(), draft, false); err != nil {
		t.Fatal(err)
	}
	view, ok := manager.Source(extensions.SourceRef{Kind: "mcp", ID: "demo"})
	if !ok || view.Enabled || view.Trust == "approved" || view.State != "disabled" {
		t.Fatalf("save unexpectedly enabled or connected source: %+v", view)
	}
	c.running = true
	if err := c.AddMCP(context.Background(), extensions.MCPDraft{ID: "other", Transport: "http", URL: "https://example.invalid/mcp"}, false); err != ErrBusy {
		t.Fatalf("running controller allowed mutation: %v", err)
	}
}

func TestEditAndRemoveMCPThroughController(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GOLDER_HOME", home)
	manager, err := extensions.New(extensions.Options{ConfigPath: filepath.Join(home, "config.toml"), Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	c := New(Options{Extensions: manager})
	initial := extensions.MCPDraft{ID: "demo", Transport: "http", URL: "https://example.invalid/mcp"}
	if err := c.AddMCP(context.Background(), initial, false); err != nil {
		t.Fatal(err)
	}
	edit, err := c.PrepareMCPEdit("demo")
	if err != nil {
		t.Fatal(err)
	}
	initial.URL = "https://new.example.invalid/mcp"
	if err := c.UpdateMCP(context.Background(), edit, initial, false); err != nil {
		t.Fatal(err)
	}
	view, _ := manager.Source(extensions.SourceRef{Kind: "mcp", ID: "demo"})
	if view.Enabled || view.Endpoint != initial.URL {
		t.Fatal("edited MCP was not saved disabled")
	}
	if err := c.RemoveMCP(context.Background(), edit); err == nil {
		t.Fatal("stale removal was accepted")
	}
	edit, err = c.PrepareMCPEdit("demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveMCP(context.Background(), edit); err != nil {
		t.Fatal(err)
	}
	if manager.HasSource(extensions.SourceRef{Kind: "mcp", ID: "demo"}) {
		t.Fatal("removed source remained in the live manager")
	}
}
