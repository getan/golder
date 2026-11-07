package extensions

import (
	"strings"
	"testing"
)

func TestEditMCPDisablesAndPreservesOtherCapabilities(t *testing.T) {
	ref := SourceRef{Kind: "mcp", ID: "edit"}
	cfg := SourceConfig{Enabled: true, Transport: "http", URL: "https://example.com/mcp", Headers: map[string]string{"Authorization": "${credential:existing}"}, AllowedTools: []string{"read*"}}
	manager := testManager(t, ref, cfg, nil)
	if err := manager.Approve(ref); err != nil {
		t.Fatal(err)
	}
	if err := manager.Enable(ref); err != nil {
		t.Fatal(err)
	}
	edit, err := manager.PrepareMCPEdit(ref.ID)
	if err != nil {
		t.Fatal(err)
	}
	draft := MCPDraft{ID: ref.ID, Transport: "http", URL: "https://new.example.com/mcp", Headers: edit.Config.Headers}
	if err := manager.UpdateMCPTarget(edit, draft); err != nil {
		t.Fatal(err)
	}
	updated, err := manager.Config(ref)
	if err != nil || updated.URL != draft.URL || updated.Enabled || updated.Headers["Authorization"] != "${credential:existing}" || len(updated.AllowedTools) != 1 {
		t.Fatalf("edit lost settings: %+v %v", updated, err)
	}
	view, _ := manager.Source(ref)
	if view.Enabled || view.Trust == "approved" {
		t.Fatal("edit retained the old authorization")
	}
	if err := manager.Enable(ref); err == nil {
		t.Fatal("edited source was enabled without review")
	}
}

func TestEditAndRemoveMCPRejectStaleConfiguration(t *testing.T) {
	ref := SourceRef{Kind: "mcp", ID: "stale"}
	cfg := SourceConfig{Transport: "http", URL: "https://example.com/mcp"}
	manager := testManager(t, ref, cfg, nil)
	edit, err := manager.PrepareMCPEdit(ref.ID)
	if err != nil {
		t.Fatal(err)
	}
	changed := cfg
	changed.URL = "https://other.example.com/mcp"
	if err := WriteSource(manager.opts.ConfigPath, ref, &changed); err != nil {
		t.Fatal(err)
	}
	draft := MCPDraft{ID: ref.ID, Transport: "http", URL: "https://new.example.com/mcp"}
	if err := manager.UpdateMCPTarget(edit, draft); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("stale edit accepted: %v", err)
	}
	if err := manager.RemoveMCP(edit); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("stale removal accepted: %v", err)
	}
	if err := manager.EnableReviewedMCP(edit, true); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("stale approval accepted: %v", err)
	}
	if _, err := manager.PrepareMCPEdit(ref.ID); err == nil {
		t.Fatal("stale in-memory source was presented as current")
	}
	onDisk, err := LoadConfig(manager.opts.ConfigPath)
	if err != nil || onDisk.MCP.Servers[ref.ID].URL != changed.URL {
		t.Fatal("stale operation changed the newer source")
	}
}

func TestRemoveMCPKeepsCredentialReferenceData(t *testing.T) {
	ref := SourceRef{Kind: "mcp", ID: "remove"}
	manager := testManager(t, ref, SourceConfig{Transport: "http", URL: "https://example.com/mcp", Headers: map[string]string{"Authorization": "${credential:shared}"}}, nil)
	edit, err := manager.PrepareMCPEdit(ref.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.RemoveMCP(edit); err != nil {
		t.Fatal(err)
	}
	if manager.HasSource(ref) {
		t.Fatal("removed source remained live")
	}
	cfg, err := LoadConfig(manager.opts.ConfigPath)
	if err != nil || len(cfg.MCP.Servers) != 0 {
		t.Fatal("removed source remained in configuration")
	}
}

func TestEditMCPEncryptsReplacementHeaders(t *testing.T) {
	ref := SourceRef{Kind: "mcp", ID: "replace"}
	manager := testManager(t, ref, SourceConfig{Transport: "http", URL: "https://example.com/mcp"}, nil)
	edit, err := manager.PrepareMCPEdit(ref.ID)
	if err != nil {
		t.Fatal(err)
	}
	secret := "fixture-" + "new-private-value"
	draft := MCPDraft{ID: ref.ID, Transport: "http", URL: "https://example.com/mcp", Headers: map[string]string{"Authorization": "Bearer " + secret}}
	if err := manager.UpdateMCPTarget(edit, draft); err != nil {
		t.Fatal(err)
	}
	cfg, err := manager.Config(ref)
	if err != nil || strings.Contains(cfg.Headers["Authorization"], secret) || !strings.HasPrefix(cfg.Headers["Authorization"], "${credential:") {
		t.Fatal("replacement header was not converted to a named credential")
	}
	resolved, err := resolveValue(cfg.Headers["Authorization"])
	if err != nil || resolved != "Bearer "+secret {
		t.Fatal("replacement header did not resolve")
	}
}
