package permissionruntime

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/getan/golder/internal/grants"
)

func TestInitializeLoadsRememberedAndImportsEnvironmentOnce(t *testing.T) {
	grants.SetProcess(nil)
	t.Cleanup(func() { grants.SetProcess(nil) })
	home, cache, extra := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("GOLDER_HOME", home)
	t.Setenv("GOLDER_READABLE_ROOTS", extra)
	s, err := grants.New(grants.Options{Path: filepath.Join(home, "grants.json")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(grants.Grant{Path: cache, Allow: grants.Read, Scope: grants.Subtree, Lifetime: grants.Remembered}); err != nil {
		t.Fatal(err)
	}
	r, err := Initialize(os.Getenv)
	if err != nil || !r.Ready || !grants.Allows(cache, grants.Read) || !grants.Allows(extra, grants.Read) {
		t.Fatalf("initialization: %+v %v", r, err)
	}
	first := grants.Process()
	if _, err := grants.Refuse(cache, grants.Write, grants.Session); err != nil {
		t.Fatal(err)
	}
	if _, err := Initialize(os.Getenv); err != nil || grants.Process() != first || grants.Check(cache, grants.Write).Decision != grants.Denied {
		t.Fatalf("reinitialization reset the live table: %v", err)
	}
}

func TestInitializeFailureIsNotAnEmptyReadyTable(t *testing.T) {
	grants.SetProcess(nil)
	t.Cleanup(func() { grants.SetProcess(nil) })
	home := t.TempDir()
	t.Setenv("GOLDER_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "grants.json"), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := Initialize(os.Getenv)
	if err == nil || r.Ready || grants.Process() != nil {
		t.Fatalf("invalid store became ready: %+v %v", r, err)
	}
}
