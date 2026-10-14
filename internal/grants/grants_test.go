package grants

// Tests for the unified table. The first block pins the property the whole
// design exists for: a migration must never hand out a capability the user did
// not already have. A migration that widens is the failure nobody sees.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// newStore opens a table in a temp dir, with no legacy migration unless the
// test asks for it.
func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(Options{Path: filepath.Join(t.TempDir(), "grants.json")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// ---- The capability model ------------------------------------------------

func TestCapabilityParsingAndRendering(t *testing.T) {
	cases := []struct {
		in   string
		want Capability
		ok   bool
	}{
		{"r", Read, true},
		{"w", Write, true},
		{"e", Execute, true},
		{"rw", ReadWrite, true},
		{"wr", ReadWrite, true}, // order is irrelevant
		{"rwe", Full, true},
		{"ewr", Full, true},
		{"read", Read, true},
		{"write", Write, true},
		{"execute", Execute, true},
		{"all", Full, true},
		{"", 0, false},
		{"x", 0, false},  // unknown letter must not be ignored
		{"rq", 0, false}, // nor a partially-valid set
		{"readw", 0, false},
	}
	for _, c := range cases {
		got, ok := ParseCapability(c.in)
		if ok != c.ok {
			t.Errorf("ParseCapability(%q) ok = %v, want %v", c.in, ok, c.ok)
			continue
		}
		if ok && got != c.want {
			t.Errorf("ParseCapability(%q) = %v, want %v", c.in, got, c.want)
		}
	}
	// Rendering is stable and round-trips.
	for _, c := range []Capability{Read, Write, Execute, ReadWrite, Full} {
		s := c.String()
		back, ok := ParseCapability(s)
		if !ok || back != c {
			t.Errorf("%v renders as %q which parses back as %v (ok=%v)", c, s, back, ok)
		}
	}
	if got := Full.String(); got != "rwe" {
		t.Errorf("Full renders as %q, want \"rwe\"", got)
	}
}

func TestWriteImpliesRead(t *testing.T) {
	s := newStore(t)
	// A write grant must be readable back: a file you may write but not read is
	// a half-failure, and every store this replaces agreed.
	if _, err := s.Add(Grant{Path: "/tmp/scratch", Allow: Write}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !s.Allows("/tmp/scratch/f", Read) {
		t.Error("a write grant did not imply read")
	}
	if !s.Allows("/tmp/scratch/f", Write) {
		t.Error("a write grant did not grant write")
	}
}

func TestExecuteIsNotImpliedByWrite(t *testing.T) {
	s := newStore(t)
	if _, err := s.Add(Grant{Path: "/tmp/scratch", Allow: ReadWrite}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// Being allowed to edit files somewhere is not being allowed to run
	// arbitrary programs there. This is the distinction that keeps a migrated
	// trust decision from widening.
	if s.Allows("/tmp/scratch", Execute) {
		t.Error("a read+write grant implied execute")
	}
}

// ---- Most-specific-wins, and denials -------------------------------------

func TestMostSpecificRowWins(t *testing.T) {
	s := newStore(t)
	mustAdd(t, s, Grant{Path: "/a", Allow: Full})
	mustAdd(t, s, Grant{Path: "/a/b", Deny: Execute})

	if !s.Allows("/a/c", Read) {
		t.Error("a grant on the parent did not cover an unrelated child")
	}
	// The refusal on the child must beat the grant inherited from the parent —
	// otherwise a denial could never be recorded under a trusted tree, which is
	// exactly the case a user creates by distrusting one directory.
	v := s.Check("/a/b/deep", Execute)
	if v.Decision != Denied {
		t.Errorf("Check(/a/b/deep, execute) = %v, want denied", v.Decision)
	}
	if v.By != "/a/b" {
		t.Errorf("denied by %q, want /a/b", v.By)
	}
	// Read is unaffected by the execute refusal.
	if !s.Allows("/a/b/deep", Read) {
		t.Error("an execute refusal also blocked read")
	}
}

func TestExactScopeCoversOnlyItsPath(t *testing.T) {
	s := newStore(t)
	mustAdd(t, s, Grant{Path: "/etc/hosts", Allow: Read, Scope: Exact})
	if !s.Allows("/etc/hosts", Read) {
		t.Error("the exact path was not allowed")
	}
	// Approving one read must not hand over the directory it lives in.
	if s.Allows("/etc/shadow", Read) {
		t.Error("an exact grant leaked to a sibling")
	}
}

func TestUndecidedIsNotAllowed(t *testing.T) {
	s := newStore(t)
	if s.Allows("/nowhere", Read) {
		t.Error("an empty table allowed a read")
	}
	if got := s.Check("/nowhere", Read).Decision; got != Undecided {
		t.Errorf("Decision = %v, want undecided", got)
	}
	// A zero capability asks a question with no meaning and must not match.
	mustAdd(t, s, Grant{Path: "/x", Allow: Full})
	if s.Allows("/x", 0) {
		t.Error("a zero capability was treated as a grant")
	}
}

// ---- Layers --------------------------------------------------------------

func TestSessionGrantOutranksSavedAndIsNotPersisted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "grants.json")
	s, err := New(Options{Path: path})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mustAdd(t, s, Grant{Path: "/data", Allow: Read, Lifetime: Session})
	if !s.Allows("/data/x", Read) {
		t.Fatal("the session grant did not apply")
	}

	// Reopen: a session grant must be gone, or "this session" would outlive the
	// process that promised it.
	reopened, err := New(Options{Path: path})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if reopened.Allows("/data/x", Read) {
		t.Error("a session grant survived a restart")
	}
}

func TestRememberedGrantSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grants.json")
	s, err := New(Options{Path: path})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mustAdd(t, s, Grant{Path: "/data", Allow: ReadWrite})

	reopened, err := New(Options{Path: path})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if !reopened.Allows("/data/x", Write) {
		t.Error("a remembered grant did not survive a restart")
	}
	if !reopened.Loaded() {
		t.Error("Loaded() = false after a reopen")
	}
}

func TestSeedLayerIsConsultedButNeverWritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grants.json")
	s, err := New(Options{Path: path})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.AddSeed(Grant{Path: "/seeded", Allow: Read}); err != nil {
		t.Fatalf("AddSeed: %v", err)
	}
	if !s.Allows("/seeded", Read) {
		t.Fatal("the seed row was not consulted")
	}
	// The seed belongs to config.toml; this store must not rewrite it.
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read: %v", err)
	}
	if len(data) > 0 {
		t.Errorf("AddSeed wrote the managed file: %s", data)
	}
	rem, err := s.Remove("/seeded")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if !rem.Seed {
		t.Error("Remove did not report the seed layer")
	}
	if rem.Persisted {
		t.Error("Remove rewrote the file for a seed-only row")
	}
}

// ---- Persistence failure -------------------------------------------------

func TestPersistenceFailureDegradesToSessionAndRollsBack(t *testing.T) {
	// A directory that cannot be written to stands in for a full disk or a
	// read-only config dir. The grant must still work for this run, and the
	// table must not claim it was remembered.
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "grants.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(path), 0o700) })

	s, err := New(Options{Path: path})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := s.Add(Grant{Path: "/data", Allow: Read, Lifetime: Remembered})
	if err != nil {
		t.Fatalf("Add returned an error instead of degrading: %v", err)
	}
	if got.Lifetime != Session {
		t.Errorf("grant lifetime = %v, want session after a failed write", got.Lifetime)
	}
	if !s.Allows("/data/x", Read) {
		t.Error("the degraded grant did not apply to this session")
	}
	// The in-memory row must not survive as remembered: claiming persistence
	// that did not happen is a lie the next run exposes.
	for _, g := range s.Entries().Remembered {
		if g.Path == "/data" {
			t.Error("a failed write left a remembered row in memory")
		}
	}
	if len(s.Warnings()) == 0 {
		t.Error("a failed write produced no warning")
	}
}

// ---- Migration must not widen --------------------------------------------

func TestMigrateTrustGrantsExecuteNotReadWrite(t *testing.T) {
	dir := t.TempDir()
	trust := filepath.Join(dir, "trust.json")
	// `true` meant "run side-effect tools here without asking". It never meant
	// "any tool may rewrite these files", so the migration must not produce a
	// write grant.
	if err := os.WriteFile(trust, []byte(`{"/work/repo": true}`), 0o600); err != nil {
		t.Fatalf("write trust: %v", err)
	}
	s, err := New(Options{Path: filepath.Join(dir, "grants.json")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	res, err := s.Migrate(LegacyPaths{Trust: trust})
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if len(res.Imported) != 1 {
		t.Fatalf("imported %d rows, want 1", len(res.Imported))
	}
	if !s.Allows("/work/repo", Execute) {
		t.Error("a trusted directory did not carry execute")
	}
	if s.Allows("/work/repo", Write) {
		t.Error("migration invented a write grant from a trust decision")
	}
	if s.Allows("/work/repo", Read) {
		t.Error("migration invented a read grant from a trust decision")
	}
}

func TestMigrateTrustPreservesAnExplicitRefusal(t *testing.T) {
	dir := t.TempDir()
	trust := filepath.Join(dir, "trust.json")
	if err := os.WriteFile(trust, []byte(`{"/work/repo": false}`), 0o600); err != nil {
		t.Fatalf("write trust: %v", err)
	}
	s, err := New(Options{Path: filepath.Join(dir, "grants.json")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := s.Migrate(LegacyPaths{Trust: trust}); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// A refusal is a decision. Dropping it would make the next run ask again at
	// best, and proceed at worst.
	if got := s.Check("/work/repo", Execute).Decision; got != Denied {
		t.Errorf("Check = %v, want denied", got)
	}
}

func TestMigrateNullIsNotADecision(t *testing.T) {
	dir := t.TempDir()
	trust := filepath.Join(dir, "trust.json")
	if err := os.WriteFile(trust, []byte(`{"/work/repo": null}`), 0o600); err != nil {
		t.Fatalf("write trust: %v", err)
	}
	s, err := New(Options{Path: filepath.Join(dir, "grants.json")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := s.Migrate(LegacyPaths{Trust: trust}); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// null was "recorded but undecided": not a grant, and not a refusal either.
	if got := s.Check("/work/repo", Execute).Decision; got != Undecided {
		t.Errorf("Check = %v, want undecided", got)
	}
}

func TestMigratePermissionsKeepsReadableReadOnly(t *testing.T) {
	dir := t.TempDir()
	perms := filepath.Join(dir, "permissions.toml")
	body := "writable_roots = [\"/cache/go-build\"]\nreadable_roots = [\"/data/inputs\"]\n"
	if err := os.WriteFile(perms, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	s, err := New(Options{Path: filepath.Join(dir, "grants.json")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := s.Migrate(LegacyPaths{Permissions: perms}); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if !s.Allows("/cache/go-build/x", Write) {
		t.Error("a writable root lost its write grant")
	}
	if !s.Allows("/data/inputs/x", Read) {
		t.Error("a readable root lost its read grant")
	}
	// The one mistake this parser must never make: a readable root arriving as
	// read+write, which would hand the user a write grant they never gave.
	if s.Allows("/data/inputs/x", Write) {
		t.Error("a readable root was migrated as writable")
	}
}

func TestMigrateSeedReadsThePermissionsTableUnderItsSection(t *testing.T) {
	dir := t.TempDir()
	seed := filepath.Join(dir, "config.toml")
	body := "[permissions]\nwritable_roots = [\"/cache/rust\"]\nreadable_roots = [\"/data\"]\n"
	if err := os.WriteFile(seed, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	s, err := New(Options{Path: filepath.Join(dir, "grants.json")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := s.Migrate(LegacyPaths{Seed: seed}); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if !s.Allows("/cache/rust/x", Write) {
		t.Error("config.toml's [permissions] writable_roots was not read")
	}
	if !s.Allows("/data/x", Read) {
		t.Error("config.toml's [permissions] readable_roots was not read")
	}
}

func TestMigrateIsSkippedOnceTheManagedFileExists(t *testing.T) {
	dir := t.TempDir()
	trust := filepath.Join(dir, "trust.json")
	if err := os.WriteFile(trust, []byte(`{"/old/repo": true}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	path := filepath.Join(dir, "grants.json")
	s, err := New(Options{Path: path})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := s.Migrate(LegacyPaths{Trust: trust}); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// The user revokes it in the new table.
	if _, err := s.Remove("/old/repo"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// A later run must not re-import from the stale legacy file: that would
	// resurrect exactly the grant the user revoked.
	reopened, err := New(Options{Path: path})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	res, err := reopened.Migrate(LegacyPaths{Trust: trust})
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if len(res.Imported) != 0 {
		t.Errorf("re-migrated %d rows from a stale file", len(res.Imported))
	}
	if reopened.Allows("/old/repo", Execute) {
		t.Error("a revoked grant came back from the legacy file")
	}
}

func TestMigrateToleratesAMalformedLegacyFile(t *testing.T) {
	dir := t.TempDir()
	trust := filepath.Join(dir, "trust.json")
	if err := os.WriteFile(trust, []byte(`{not json`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	s, err := New(Options{Path: filepath.Join(dir, "grants.json")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// A corrupt old file must not stop golder from starting; it is reported.
	res, err := s.Migrate(LegacyPaths{Trust: trust})
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if len(res.Warnings) == 0 {
		t.Error("a malformed legacy file produced no warning")
	}
}

// ---- Store integrity -----------------------------------------------------

func TestUnknownSchemaAndCapabilitiesAreRefused(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		body string
	}{
		{"newer version", `{"version":99,"grants":[]}`},
		{"unknown capability letter", `{"version":1,"grants":[{"path":"/x","allow":"rq"}]}`},
		{"row with nothing", `{"version":1,"grants":[{"path":"/x"}]}`},
		{"missing path", `{"version":1,"grants":[{"allow":"r"}]}`},
		{"unknown scope", `{"version":1,"grants":[{"path":"/x","allow":"r","scope":"galaxy"}]}`},
		{"persisted session row", `{"version":1,"grants":[{"path":"/x","allow":"r","lifetime":"session"}]}`},
		{"not json", `{`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(dir, "grants.json")
			if err := os.WriteFile(path, []byte(c.body), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			// Refusing to load beats loading half a permission store.
			if _, err := New(Options{Path: path}); err == nil {
				t.Errorf("New accepted %s", c.name)
			}
		})
	}
}

func TestAddMergesRatherThanShadowing(t *testing.T) {
	s := newStore(t)
	mustAdd(t, s, Grant{Path: "/x", Allow: Read})
	mustAdd(t, s, Grant{Path: "/x", Allow: Write})
	if !s.Allows("/x", Read) || !s.Allows("/x", Write) {
		t.Error("a second grant for the same path dropped the first")
	}
	// A later grant for a bit that was refused clears the refusal: that is what
	// approving something previously denied means.
	mustAdd(t, s, Grant{Path: "/y", Deny: Execute})
	if got := s.Check("/y", Execute).Decision; got != Denied {
		t.Fatalf("Check = %v, want denied", got)
	}
	mustAdd(t, s, Grant{Path: "/y", Allow: Execute})
	if got := s.Check("/y", Execute).Decision; got != Allowed {
		t.Errorf("Check after re-approval = %v, want allowed", got)
	}
}

func TestRemoveRevokesEveryLayer(t *testing.T) {
	s := newStore(t)
	mustAdd(t, s, Grant{Path: "/x", Allow: Full})
	mustAdd(t, s, Grant{Path: "/x", Allow: Read, Lifetime: Session})
	// The remembered row grants it, so the path is NOT session-only: a dialog
	// asking "remember this?" must not be offered for something already
	// durable. SessionOnly answers exactly that question.
	if s.SessionOnly("/x", Read) {
		t.Error("a path with a remembered grant was reported as session-only")
	}
	rem, err := s.Remove("/x")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if !rem.Session || !rem.Remembered || !rem.Persisted {
		t.Errorf("Removal = %+v, want every layer touched and persisted", rem)
	}
	if s.Allows("/x", Full) {
		t.Error("a removed path is still allowed")
	}
	// With the remembered row gone too, the session row alone would make it
	// session-only; both are gone, so nothing grants it at all.
	if s.SessionOnly("/x", Read) {
		t.Error("a fully removed path was reported as session-only")
	}
}

func TestSessionOnlyReportsTheLayerThatGranted(t *testing.T) {
	s := newStore(t)
	mustAdd(t, s, Grant{Path: "/saved", Allow: Read})
	mustAdd(t, s, Grant{Path: "/live", Allow: Read, Lifetime: Session})
	if s.SessionOnly("/saved", Read) {
		t.Error("a remembered grant was reported as session-only")
	}
	if !s.SessionOnly("/live", Read) {
		t.Error("a session grant was not reported as session-only")
	}
	if s.SessionOnly("/absent", Read) {
		t.Error("an absent path was reported as session-only")
	}
}

func TestOnDiskShapeIsHandEditable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grants.json")
	s, err := New(Options{Path: path})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mustAdd(t, s, Grant{Path: "/work", Allow: ReadWrite, Source: "test"})

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var f fileJSON
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("the file is not valid JSON: %v", err)
	}
	if f.Version != CurrentVersion {
		t.Errorf("version = %d, want %d", f.Version, CurrentVersion)
	}
	if len(f.Grants) != 1 || f.Grants[0].Allow != "rw" {
		t.Fatalf("grants = %+v, want one row with allow \"rw\"", f.Grants)
	}
	// A written file must be readable by the same parser that guards loading.
	if _, err := parseFile(data); err != nil {
		t.Errorf("the writer produced a file the reader rejects: %v", err)
	}
}

func TestAddRejectsNonsense(t *testing.T) {
	s := newStore(t)
	if _, err := s.Add(Grant{Path: "", Allow: Read}); err == nil {
		t.Error("an empty path was accepted")
	}
	if _, err := s.Add(Grant{Path: "/x"}); err == nil {
		t.Error("a row with no capability was accepted")
	}
	if _, err := s.Add(Grant{Path: "relative/path", Allow: Read}); err == nil {
		t.Error("a relative path was accepted")
	}
}

func TestConcurrentAccess(t *testing.T) {
	s := newStore(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			_, _ = s.Add(Grant{Path: "/concurrent", Allow: Read, Lifetime: Session})
			_ = s.Allows("/concurrent", Read)
			_ = s.Entries()
		}
	}()
	for i := 0; i < 200; i++ {
		_ = s.Allows("/concurrent", Read)
		_ = s.Check("/concurrent", Write)
		_ = s.Entries()
	}
	<-done
}

func TestTwoStoresShareOneFileWithoutLosingGrants(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grants.json")
	a, err := New(Options{Path: path})
	if err != nil {
		t.Fatalf("New a: %v", err)
	}
	b, err := New(Options{Path: path})
	if err != nil {
		t.Fatalf("New b: %v", err)
	}
	// Both processes answer a dialog without seeing each other's write. Each
	// grant must survive: a permission store that drops one silently is worse
	// than one that refuses to write.
	mustAdd(t, a, Grant{Path: "/from-a", Allow: Read})
	mustAdd(t, b, Grant{Path: "/from-b", Allow: Read})

	final, err := New(Options{Path: path})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if !final.Allows("/from-a", Read) {
		t.Error("the first process's grant was lost")
	}
	if !final.Allows("/from-b", Read) {
		t.Error("the second process's grant was lost")
	}
}

func mustAdd(t *testing.T, s *Store, g Grant) Grant {
	t.Helper()
	out, err := s.Add(g)
	if err != nil {
		t.Fatalf("Add(%+v): %v", g, err)
	}
	return out
}
