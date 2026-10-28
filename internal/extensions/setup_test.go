package extensions

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/extension"
	setupevent "github.com/getan/golder/extension/setup"
	"github.com/getan/golder/internal/seatbelt"
)

func TestSetupFixtureChild(t *testing.T) {
	if os.Getenv("EXTENSION_SETUP_FIXTURE") != "1" {
		return
	}
	home := os.Getenv("HOME")
	protected := os.Getenv("EXTENSION_TEST_PROTECTED")
	for _, path := range []string{filepath.Join(protected, "credentials.json"), filepath.Join(protected, "extensions", "mcp-other", "home", "secret")} {
		if _, err := os.ReadFile(path); err == nil {
			fmt.Fprintln(os.Stderr, "sibling/host state was readable")
			os.Exit(3)
		}
	}
	marker := filepath.Join(home, "refresh-state")
	old, _ := os.ReadFile(marker)
	if err := os.WriteFile(marker, append(old, 'x'), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(4)
	}
	url := os.Getenv("EXTENSION_TEST_URL")
	if url == "" {
		url = "https://auth.example.com/device?code=operator-only"
	}
	encoder := json.NewEncoder(os.Stdout)
	_ = encoder.Encode(setupevent.Event{Version: 1, Type: "authorization", Service: "fixture", VerificationURL: url, ExpiresAt: time.Now().Add(time.Minute)})
	_ = encoder.Encode(setupevent.Event{Version: 1, Type: "complete", Service: "fixture", Authenticated: true})
	os.Exit(0)
}

func TestPersistentSetupIsSourceScopedAndResettable(t *testing.T) {
	if !seatbelt.Available() {
		t.Skip("OS sandbox unavailable")
	}
	exe, _ := os.Executable()
	ref := SourceRef{Kind: "mcp", ID: "fixture"}
	c := SourceConfig{Enabled: true, Transport: "stdio", Command: exe, StateDir: true,
		Setup: []string{exe, "-test.run=^TestSetupFixtureChild$"}, SetupOrigins: []string{"https://auth.example.com"},
		Env: map[string]string{"EXTENSION_SETUP_FIXTURE": "1"}}
	m := testManager(t, ref, c, nil)
	root := os.Getenv("GOLDER_HOME")
	s, _ := m.get(ref)
	s.cfg.Env["EXTENSION_TEST_PROTECTED"] = root
	// Mutating configuration must not retain approval: construct a fresh manager
	// with the final reviewed fixture settings before any child can run.
	c.Env["EXTENSION_TEST_PROTECTED"] = root
	if err := WriteSource(m.opts.ConfigPath, ref, &c); err != nil {
		t.Fatal(err)
	}
	_ = m.Close()
	m, err := New(Options{ConfigPath: m.opts.ConfigPath, Cwd: m.opts.Cwd})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err = m.ApproveSession(ref); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(root, "extensions", "mcp-other", "home")
	if err = os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(root, "credentials.json"), filepath.Join(other, "secret")} {
		if err = os.WriteFile(path, []byte("not-for-the-child"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		events := 0
		if err = m.Setup(context.Background(), ref, func(e setupevent.Event) error { events++; return nil }); err != nil {
			t.Fatalf("setup %d: %v", i, err)
		}
		if events != 2 {
			t.Fatalf("events=%d", events)
		}
	}
	home := sourceHome(ref)
	data, err := os.ReadFile(filepath.Join(home, "refresh-state"))
	if err != nil || string(data) != "xx" {
		t.Fatalf("state did not survive restart: %q %v", data, err)
	}
	if logs, _ := m.Logs(ref); strings.Contains(logs, "operator-only") {
		t.Fatal("authorization code entered normal logs")
	}
	if err = m.Reset(ref); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(home, "refresh-state")); !os.IsNotExist(err) {
		t.Fatal("reset retained credentials")
	}
	if _, err = os.Stat(filepath.Join(other, "secret")); err != nil {
		t.Fatal("reset damaged another source")
	}
}

func TestDependencyAndSkillChangesInvalidateFingerprint(t *testing.T) {
	dir := t.TempDir()
	dependency := filepath.Join(dir, "native-cli")
	exe, _ := os.Executable()
	c := SourceConfig{Transport: "stdio", Command: exe, Executables: []string{dependency}}
	ref := SourceRef{Kind: "mcp", ID: "test"}
	before, err := fingerprint(ref, c, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(dependency, []byte("first-native-content"), 0o700); err != nil {
		t.Fatal(err)
	}
	after, err := fingerprint(ref, c, dir)
	if err != nil || before == after {
		t.Fatal("missing dependency acquired execution without a changed fingerprint", err)
	}
	if err = os.WriteFile(dependency, []byte("replaced-native-content"), 0o700); err != nil {
		t.Fatal(err)
	}
	changed, _ := fingerprint(ref, c, dir)
	if changed == after {
		t.Fatal("replaced dependency retained trust")
	}
}

func TestAggregateCannotBypassNarrowToolDeny(t *testing.T) {
	m := &Manager{}
	s := &source{ref: SourceRef{Kind: "mcp", ID: "lark"}, cfg: SourceConfig{AggregateTools: []string{"cli_call"}}, catalog: Catalog{Tools: []extension.ToolSpec{{Name: "message_send"}, {Name: "cli_call"}, {Name: "doc_read"}}}}
	if !m.toolAllowed(s, "cli_call") {
		t.Fatal("unfiltered aggregate should be available")
	}
	s.cfg.DisallowedTools = []string{"message_send"}
	if m.toolAllowed(s, "cli_call") || !m.toolAllowed(s, "doc_read") {
		t.Fatal("aggregate bypassed a narrower deny or broke unrelated reads")
	}
}
