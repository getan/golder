package workspacecmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNamedCommandGrammar(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		command Command
		name    string
	}{
		{[]string{"new", "coding", "--shell"}, CommandNew, "coding"},
		{[]string{"attach", "coding"}, CommandAttach, "coding"},
		{[]string{"-s", "coding", "attach"}, CommandAttach, "coding"},
		{[]string{"attach"}, CommandAttach, ""},
		{[]string{"list", "--json"}, CommandList, ""},
		{[]string{"status", "stop"}, CommandStatus, "stop"}, // command words can be names
		{[]string{"stop", "coding"}, CommandStop, "coding"},
		{[]string{"rm", "coding"}, CommandRemove, "coding"},
	} {
		p, err := parse(tc.args)
		if err != nil || p.command != tc.command || p.session != tc.name {
			t.Errorf("parse(%q) = %+v, %v", tc.args, p, err)
		}
	}
	for _, args := range [][]string{
		{"new"}, {"list", "coding"}, {"--json"}, {"attach", "coding", "-C", "/tmp"},
		{"rm", "coding", "--headless"}, {"new", "one", "-s", "two"},
		{"attach", "coding", "--", "--model", "x"}, {"list", "--socket", "/tmp/x"},
		{"new", "--legacy", "123456789abc/old"}, {"rm", "--legacy", "123456789abc/old"},
	} {
		if _, err := parse(args); err == nil {
			t.Errorf("parse(%q) should reject incompatible/ambiguous arguments", args)
		}
	}
}

func TestGlobalNameValidation(t *testing.T) {
	for _, name := range []string{"default", "coding", "1", "work-2", "a_b", strings.Repeat("x", 64)} {
		if err := validateName(name); err != nil {
			t.Errorf("%q: %v", name, err)
		}
	}
	for _, name := range []string{"", "Work", "a b", "a.b", "../x", "a/b", "a\\b", "-work", "_work", "研发", "a\nb", strings.Repeat("x", 65)} {
		if err := validateName(name); err == nil {
			t.Errorf("unsafe/noncanonical name %q was accepted", name)
		}
	}
}

func TestRecordedSessionDoesNotConsultCwdOrRuntime(t *testing.T) {
	e := newTestEnv(t)
	first := mustResolve(t, e, "-s", "coding")
	if err := SaveSession(first); err != nil {
		t.Fatal(err)
	}
	disk, err := os.ReadFile(first.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(e.project); err != nil {
		t.Fatal(err)
	}
	e.set("XDG_RUNTIME_DIR", filepath.Join(e.base, "another-run"))
	deps := e.deps()
	deps.Getwd = func() (string, error) { return "", errors.New("caller cwd was deleted") }
	for _, command := range []string{"run", "attach", "status", "stop", "rm"} {
		p, _ := parse([]string{command, "-s", "coding"})
		opts, err := resolve(p, deps)
		if err != nil {
			t.Fatalf("%s: %v", command, err)
		}
		if !opts.Exists || opts.Cwd != first.Cwd || opts.Socket != first.Socket || opts.StateDir != first.StateDir || opts.SocketDir != first.SocketDir {
			t.Errorf("%s lost the recorded identity: %+v", command, opts)
		}
	}
	after, _ := os.ReadFile(first.ManifestPath)
	if string(disk) != string(after) {
		t.Fatal("lookups rewrote the record")
	}
	other := newTestEnv(t)
	otherOpts := mustResolve(t, other, "-s", "coding")
	if first.Socket == otherOpts.Socket {
		t.Fatal("different GOLDER_HOME roots share a socket")
	}
}

func TestRecordPreservesCreationTimeAndRejectsRetargeting(t *testing.T) {
	e := newTestEnv(t)
	opts := mustResolve(t, e)
	opts.CreatedAt = "2026-01-02T03:04:05Z"
	if err := SaveSession(opts); err != nil {
		t.Fatal(err)
	}
	before, _, err := loadRecord(opts)
	if err != nil {
		t.Fatal(err)
	}
	opts.CreatedAt = "2026-02-02T03:04:05Z"
	if err := SaveSession(opts); err != nil {
		t.Fatal(err)
	}
	after, _, _ := loadRecord(opts)
	if before.CreatedAt != after.CreatedAt {
		t.Fatal("save reset the creation timestamp")
	}
	for _, args := range [][]string{
		{"--socket", filepath.Join(e.base, "other.sock")},
		{"--state-dir", filepath.Join(e.base, "other-state")},
		{"--config", filepath.Join(e.base, "other.toml")},
	} {
		if _, err := resolved(t, e, args...); err == nil {
			t.Errorf("retarget accepted: %q", args)
		}
	}
	st, err := os.Stat(opts.ManifestPath)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("record permissions: %v, %v", st, err)
	}
}

func TestLookupAndCreationRefuseSymlinkedCatalog(t *testing.T) {
	for _, at := range []string{"workspace", "workspace/sessions", "workspace/sessions/default"} {
		t.Run(at, func(t *testing.T) {
			e := newTestEnv(t)
			target := filepath.Join(e.base, "outside")
			if err := os.MkdirAll(target, 0o755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(e.home, at)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Skip(err)
			}
			if _, err := resolved(t, e, "status"); err == nil {
				t.Fatal("lookup followed a catalog symlink")
			}
			if _, err := resolved(t, e); err == nil {
				t.Fatal("creation followed a catalog symlink")
			}
			st, _ := os.Stat(target)
			if st.Mode().Perm() != 0o755 {
				t.Fatal("the target directory was chmodded")
			}
			entries, _ := os.ReadDir(target)
			if len(entries) != 0 {
				t.Fatal("lookup/start wrote through the link")
			}
		})
	}
}

func writeLegacyFixture(t *testing.T, e *testEnv) string {
	t.Helper()
	id := projectID(e.project)
	dir := filepath.Join(e.home, "workspace", id, "sessions", "Old Work")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(e.base, "old.sock")
	record := map[string]any{
		"project": e.project, "session": "Old Work", "socket": socket,
		"config":    filepath.Join(e.home, "workspace", id, "config.toml"),
		"state_dir": filepath.Join(dir, "custom-state"), "log": filepath.Join(dir, "workspace.log"),
		"token": "this-proof-must-not-appear-in-list",
	}
	sum := sha256.Sum256([]byte(socket))
	body, _ := json.Marshal(record)
	if err := os.WriteFile(filepath.Join(dir, "owner-"+hex.EncodeToString(sum[:8])+".json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	return id + "/Old Work"
}

func TestListIncludesLegacyAndBrokenRecordsWithoutLeakingProof(t *testing.T) {
	e := newTestEnv(t)
	for _, name := range []string{"default", "coding", "broken"} {
		o := mustResolve(t, e, "-s", name)
		if err := SaveSession(o); err != nil {
			t.Fatal(err)
		}
		if name == "broken" {
			if err := os.WriteFile(o.ManifestPath, []byte("bad json"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	selector := writeLegacyFixture(t, e)
	opts := mustResolve(t, e, "status", "--legacy", selector)
	if !opts.Legacy || opts.Cwd != e.project || filepath.Base(opts.StateDir) != "custom-state" {
		t.Fatalf("legacy resolve = %+v", opts)
	}
	rec := &recorder{status: func(_ context.Context, o Options) ([]byte, error) {
		if o.Session == "default" {
			return []byte(`{"running":true,"panes":{"windows":[{}]}}`), nil
		}
		if o.Session == "coding" {
			return nil, errors.New("connection timed out")
		}
		return []byte(`{"running":false}`), nil
	}}
	out, stderr, code := e.run(t, rec, "list", "--json")
	if code != 0 {
		t.Fatalf("list: %s", stderr)
	}
	var result struct {
		Sessions []listedSession `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Sessions) != 4 {
		t.Fatalf("list = %s", out)
	}
	seen := map[string]listedSession{}
	for _, row := range result.Sessions {
		seen[row.Selector] = row
	}
	if seen["broken"].State != "unreachable" || seen["coding"].Running != nil {
		t.Fatalf("failures reported as stopped: %s", out)
	}
	if seen["default"].Panes == nil || *seen["default"].Panes != 1 || !seen[selector].Legacy {
		t.Fatalf("missing global/legacy information: %s", out)
	}
	if strings.Contains(out, "this-proof-must-not-appear") {
		t.Fatal("list leaked an owner proof")
	}
	for _, got := range rec.statused {
		if got.Session == "broken" {
			t.Fatal("a corrupt record was used to probe an endpoint")
		}
	}
}

func TestEmptyListCreatesNothing(t *testing.T) {
	e := newTestEnv(t)
	rec := &recorder{}
	out, stderr, code := e.run(t, rec, "list", "--json")
	if code != 0 || strings.TrimSpace(out) != "{\n  \"sessions\": []\n}" {
		t.Fatalf("empty list: %q / %s (%d)", out, stderr, code)
	}
	if rec.calls() != 0 {
		t.Fatal("empty list queried a daemon")
	}
	if _, err := os.Stat(filepath.Join(e.home, "workspace")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("list created state")
	}
}
