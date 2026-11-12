package extensions

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/extension"
)

type larkStatusTestBackend struct {
	calls []extension.Call
}

func (*larkStatusTestBackend) Catalog(context.Context) (Catalog, error) { return Catalog{}, nil }
func (b *larkStatusTestBackend) Call(_ context.Context, call extension.Call, _ func(extension.Progress)) (extension.Result, error) {
	b.calls = append(b.calls, call)
	return extension.Text(`{"ok":true,"data":{"services":{"lark":{"installed":true,"authenticated":true,"details":{"identity":"user","verified":true}},"project":{"installed":false}}}}`), nil
}
func (*larkStatusTestBackend) GetPrompt(context.Context, string, map[string]string) (string, error) {
	return "", nil
}
func (*larkStatusTestBackend) ReadResource(context.Context, string) (string, error) { return "", nil }
func (*larkStatusTestBackend) Close() error                                         { return nil }

func TestLarkAuthorizationProbeUsesOnlyApprovedFixedStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), executableName("golder-lark"))
	contents := []byte("fixture executable")
	if err := os.WriteFile(path, contents, 0o700); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("GOLDER_HOME", home)
	sum := sha256.Sum256(contents)
	pkg, err := InstallManagedPackage(PackageInstallOptions{ID: "lark", Version: "1.2.3", Source: path, SHA256: fmt.Sprintf("%x", sum)})
	if err != nil {
		t.Fatal(err)
	}
	ref := SourceRef{Kind: "mcp", ID: "lark"}
	configPath := filepath.Join(home, "config.toml")
	if err := WriteSource(configPath, ref, &SourceConfig{Enabled: true, Transport: "stdio", Command: pkg.Executable}); err != nil {
		t.Fatal(err)
	}
	m, err := New(Options{ConfigPath: configPath, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	if _, err := m.ProbeLarkAuthorization(context.Background()); err == nil {
		t.Fatal("unapproved helper was executed")
	}
	if err := m.Approve(ref); err != nil {
		t.Fatal(err)
	}
	s, err := m.get(ref)
	if err != nil {
		t.Fatal(err)
	}
	backend := &larkStatusTestBackend{}
	s.mu.Lock()
	s.conn = backend
	s.life, s.cancel = context.WithCancel(context.Background())
	s.catalog.Tools = []extension.ToolSpec{{Name: "status", InputSchema: json.RawMessage(`{"type":"object"}`)}}
	s.mu.Unlock()
	got, err := m.ProbeLarkAuthorization(context.Background())
	if err != nil || got.Lark.State != "authorized" || got.Project.State != "unavailable" {
		t.Fatalf("status=%+v err=%v", got, err)
	}
	if len(backend.calls) != 1 || backend.calls[0].Tool != "status" || string(backend.calls[0].Arguments) != `{"verify":true}` || backend.calls[0].OperatorConfirmed || backend.calls[0].PolicyApproved {
		t.Fatalf("unexpected probe call: %+v", backend.calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.ProbeLarkAuthorization(ctx); err == nil || len(backend.calls) != 1 {
		t.Fatal("cancelled status probe was executed")
	}
	m.opts.Allow = func(string) bool { return false }
	if _, err := m.ProbeLarkAuthorization(context.Background()); err == nil || len(backend.calls) != 1 {
		t.Fatal("blocked status tool was executed")
	}
	if err := os.WriteFile(pkg.Executable, []byte("changed executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	m.opts.Allow = nil
	if _, err := m.ProbeLarkAuthorization(context.Background()); err == nil || len(backend.calls) != 1 {
		t.Fatal("changed helper was executed")
	}
}

func TestParseLarkAuthorizationStatusSeparatesUnknownFromLogin(t *testing.T) {
	for _, tc := range []struct {
		name, payload, lark, project string
	}{
		{"both verified", `{"ok":true,"data":{"services":{"lark":{"installed":true,"authenticated":true,"details":{"identity":"user","verified":true}},"project":{"installed":true,"authenticated":true,"details":{"authenticated":true}}}}}`, "authorized", "authorized"},
		{"project login required", `{"ok":true,"data":{"services":{"lark":{"installed":true,"authenticated":true,"details":{"identity":"user","verified":true}},"project":{"installed":true,"authenticated":false,"details":{"authenticated":false}}}}}`, "authorized", "required"},
		{"network error", `{"ok":true,"data":{"services":{"lark":{"installed":true,"authenticated":false,"details":{}},"project":{"installed":true,"authenticated":false,"error":"Network/server unavailable","details":{"authenticated":false}}}}}`, "unknown", "unknown"},
		{"no project", `{"ok":true,"data":{"services":{"lark":{"installed":true,"authenticated":false,"details":{"identity":"bot"}},"project":{"installed":false,"authenticated":false}}}}`, "required", "unavailable"},
		{"lark explicitly unverified", `{"ok":true,"data":{"services":{"lark":{"installed":true,"authenticated":false,"details":{"identity":"user","verified":false}},"project":{"installed":false}}}}`, "required", "unavailable"},
		{"unverified user", `{"ok":true,"data":{"services":{"lark":{"installed":true,"authenticated":true,"details":{"identity":"user","verified":false}},"project":{"installed":true,"authenticated":true,"details":{}}}}}`, "unknown", "authorized"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseLarkAuthorizationStatus([]byte(tc.payload))
			if err != nil || got.Lark.State != tc.lark || got.Project.State != tc.project {
				t.Fatalf("got %+v, %v; want %s/%s", got, err, tc.lark, tc.project)
			}
		})
	}
	for _, invalid := range []string{"", "{}", `{"ok":false}`, strings.Repeat("x", 64<<10+1)} {
		if _, err := parseLarkAuthorizationStatus([]byte(invalid)); err == nil {
			t.Fatalf("accepted invalid status %q", invalid[:min(len(invalid), 100)])
		}
	}
}

func TestParseLarkAuthorizationStatusMissingScopes(t *testing.T) {
	got, err := parseLarkAuthorizationStatus([]byte(`{"ok":true,"data":{"services":{"lark":{"installed":true,"authenticated":true,"missing_user_scopes":["search:docs:read"],"details":{"identity":"user","verified":true}}}}}`))
	if err != nil || got.Lark.State != "authorized" || len(got.Lark.MissingUserScopes) != 1 || got.Lark.MissingUserScopes[0] != "search:docs:read" {
		t.Fatalf("unexpected status: %+v, %v", got, err)
	}
}
