package extensions

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/getan/golder/internal/provider"
)

func TestParseMCPImport(t *testing.T) {
	raw := `{"mcpServers":{"remote":{"url":"https://example.com/mcp","headers":{"Authorization":"Bearer private-token"}},"local":{"command":"echo","args":["hello world"],"env":{"API_TOKEN":"${API_TOKEN}"}}}}`
	drafts, err := ParseMCPImport(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 2 || drafts[0].ID != "local" || drafts[0].Transport != "stdio" || drafts[0].Args[0] != "hello world" || drafts[1].Transport != "http" {
		t.Fatalf("unexpected parsed configuration: %+v", drafts)
	}
	for _, bad := range []string{
		`{"url":"https://example.com/mcp","url":"https://evil.invalid"}`,
		`{"url":"https://example.com/mcp","unknown":"private-token"}`,
		`{"url":"https://example.com/mcp?token=private-token"}`,
		`{"type":"sse","url":"https://example.com/mcp"}`,
		`{"url":"https://example.com/mcp","command":"echo"}`,
		`{"command":"echo","env":{"GOLDER_SECRET":"private-token"}}`,
		`{"command":"echo","args":["--api-key","private-token"]}`,
		`{"command":"echo","env":{"A":"prefix-${TOKEN}"}}`,
	} {
		_, err := ParseMCPImport(bad)
		if err == nil || strings.Contains(err.Error(), "private-token") {
			t.Fatalf("unsafe configuration accepted or exposed in error: %v", err)
		}
	}
}

func TestAddMCPStoresSecretsAndDoesNotOverwrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GOLDER_HOME", home)
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte("# keep\nmodel = 'demo'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := New(Options{ConfigPath: path, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	draft := MCPDraft{ID: "remote", Transport: "http", URL: "https://example.com/mcp", Headers: map[string]string{"Authorization": "Bearer secret-token"}}
	if err := m.AddMCP(draft); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(raw), "secret-token") || !strings.Contains(string(raw), "# keep") {
		t.Fatalf("unsafe configuration or lost unrelated content: %v", err)
	}
	cfg, err := m.Config(SourceRef{Kind: "mcp", ID: "remote"})
	if err != nil || cfg.Enabled || !strings.HasPrefix(cfg.Headers["Authorization"], "${credential:") {
		t.Fatalf("unexpected saved source: %+v %v", cfg, err)
	}
	value, err := resolveValue(cfg.Headers["Authorization"])
	if err != nil || value != "Bearer secret-token" {
		t.Fatal("saved credential did not resolve")
	}
	before, err := provider.CredentialNames(provider.CredentialFilePath())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.AddMCP(draft); err == nil {
		t.Fatal("duplicate source was accepted")
	}
	after, err := provider.CredentialNames(provider.CredentialFilePath())
	if err != nil || fmt.Sprint(before) != fmt.Sprint(after) {
		t.Fatalf("failed import left a credential: %v", err)
	}
}

func TestConcurrentMCPAddsPreserveBoth(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GOLDER_HOME", home)
	path := filepath.Join(home, "config.toml")
	m1, err := New(Options{ConfigPath: path, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer m1.Close()
	m2, err := New(Options{ConfigPath: path, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Close()
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i, m := range []*Manager{m1, m2} {
		wg.Add(1)
		go func(i int, m *Manager) {
			defer wg.Done()
			errs <- m.AddMCP(MCPDraft{ID: fmt.Sprintf("source-%d", i), Transport: "http", URL: "https://example.com/mcp", Headers: map[string]string{"Authorization": fmt.Sprintf("secret-%d", i)}})
		}(i, m)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := LoadConfig(path)
	if err != nil || len(cfg.MCP.Servers) != 2 {
		t.Fatalf("concurrent sources lost: %v", err)
	}
	for _, source := range cfg.MCP.Servers {
		if _, err := resolveValue(source.Headers["Authorization"]); err != nil {
			t.Fatal("concurrent credential was lost")
		}
	}
}
