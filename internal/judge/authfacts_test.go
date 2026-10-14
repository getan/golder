package judge

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/getan/golder/internal/grants"
)

// authFixture returns the read fixture plus a process-wide authorization table,
// so a test can grant and refuse the way the dialogs do.
func authFixture(t *testing.T) (workspace, outside, target string, store *grants.Store) {
	t.Helper()
	workspace, outside, target = readFixture(t)
	store, err := grants.New(grants.Options{Path: filepath.Join(t.TempDir(), "grants.json")})
	if err != nil {
		t.Fatalf("grants.New: %v", err)
	}
	grants.SetProcess(store)
	t.Cleanup(func() { grants.SetProcess(nil) })
	return workspace, outside, target, store
}

// TestAuthFactsSeparateWorkspaceFromConsent is the distinction the state exists
// for: a path inside the workspace is not a question, and a path outside it is.
// A reviewer told only "outside" would re-ask what the workspace already
// answered; one told only "inside" would miss a boundary crossing entirely.
func TestAuthFactsSeparateWorkspaceFromConsent(t *testing.T) {
	workspace, _, target, _ := authFixture(t)

	inside := AuthFacts("read", readCall("read", "a.go").Arguments, workspace, nil)
	if len(inside) != 1 {
		t.Fatalf("facts for an in-workspace read = %+v, want one", inside)
	}
	if !inside[0].InWorkspace {
		t.Errorf("a workspace path reported as outside: %+v", inside[0])
	}
	if inside[0].NeedsConsent {
		t.Errorf("a workspace path must not need consent: %+v", inside[0])
	}

	out := AuthFacts("read", readCall("read", target).Arguments, workspace, nil)
	if len(out) != 1 {
		t.Fatalf("facts for an outside read = %+v, want one", out)
	}
	if out[0].InWorkspace {
		t.Errorf("an outside path reported as in-workspace: %+v", out[0])
	}
	if !out[0].NeedsConsent {
		t.Errorf("an unapproved outside path must need consent: %+v", out[0])
	}
}

// TestAuthFactsReportTheTablesAnswer covers the other half: once the user has
// answered, the model is told what they answered. A grant settles the question;
// a refusal is reported as a refusal rather than as silence — the gate still
// asks (a refusal does not silence it yet), so the model should see both facts.
func TestAuthFactsReportTheTablesAnswer(t *testing.T) {
	workspace, outside, target, store := authFixture(t)

	if _, err := store.Add(grants.Grant{
		Path: outside, Allow: grants.Read, Scope: grants.Subtree, Lifetime: grants.Session,
	}); err != nil {
		t.Fatalf("granting %s: %v", outside, err)
	}
	granted := AuthFacts("read", readCall("read", target).Arguments, workspace, nil)
	if len(granted) == 0 {
		t.Fatal("no facts for a granted path")
	}
	if granted[0].NeedsConsent {
		t.Errorf("a granted path must not need consent: %+v", granted[0])
	}
	if granted[0].Read != "granted" {
		t.Errorf("read = %q, want granted: %+v", granted[0].Read, granted[0])
	}
	// A grant is per capability: reading is allowed, running is not.
	if granted[0].Execute == "granted" {
		t.Errorf("a read grant must not read as an execute grant: %+v", granted[0])
	}

	if _, err := store.Refuse(outside+"/other", grants.Read, grants.Session); err != nil {
		t.Fatalf("refusing: %v", err)
	}
	refused := AuthFacts("read", readCall("read", filepath.Join(outside, "other")).Arguments, workspace, nil)
	if len(refused) == 0 {
		t.Fatal("no facts for a refused path")
	}
	if refused[0].Read != "refused" {
		t.Errorf("read = %q, want refused: %+v", refused[0].Read, refused[0])
	}
}

// TestAuthFactsFollowSymlinks is the P0 escape seen from the reviewer's side:
// a link inside the workspace that points outside it must be reported as
// outside. A textual comparison would call it in-workspace, so the model would
// grade an escape as a routine in-tree read.
func TestAuthFactsFollowSymlinks(t *testing.T) {
	workspace, outside, _, _ := authFixture(t)

	link := filepath.Join(workspace, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	facts := AuthFacts("read", readCall("read", filepath.Join(link, "a.go")).Arguments, workspace, nil)
	if len(facts) != 1 {
		t.Fatalf("facts = %+v, want one", facts)
	}
	if facts[0].InWorkspace {
		t.Errorf("a link that lands outside must not report as in-workspace: %+v", facts[0])
	}
	if !facts[0].NeedsConsent {
		t.Errorf("a link that lands outside must need consent: %+v", facts[0])
	}
}

// TestAuthFactsCapabilitiesAreSeparate pins the reason the three are reported
// apart: the table keeps them apart, so a directory trusted for running
// commands must not read as authorization to rewrite it.
func TestAuthFactsCapabilitiesAreSeparate(t *testing.T) {
	workspace, outside, target, store := authFixture(t)

	if _, err := store.Add(grants.Grant{
		Path: outside, Allow: grants.Execute, Scope: grants.Subtree, Lifetime: grants.Session,
	}); err != nil {
		t.Fatalf("granting execute: %v", err)
	}
	facts := AuthFacts("read", readCall("read", target).Arguments, workspace, nil)
	if len(facts) == 0 {
		t.Fatal("no facts")
	}
	if facts[0].Execute != "granted" {
		t.Errorf("execute = %q, want granted: %+v", facts[0].Execute, facts[0])
	}
	if facts[0].Read == "granted" {
		t.Errorf("an execute grant must not read as a read grant: %+v", facts[0])
	}
	if !facts[0].NeedsConsent {
		t.Errorf("reading an execute-only directory still needs consent: %+v", facts[0])
	}
}

// TestAuthFactsForAToolWithNoPaths pins the empty case: a call that names no
// path gets no facts rather than a fabricated entry a model might reason from.
func TestAuthFactsForAToolWithNoPaths(t *testing.T) {
	workspace, _, _, _ := authFixture(t)
	if got := AuthFacts("todo", []byte(`{"todos":[]}`), workspace, nil); len(got) != 0 {
		t.Errorf("facts for a pathless tool = %+v, want none", got)
	}
}
