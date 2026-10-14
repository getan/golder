package golderhome

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// envFrom builds a getenv over a map, so a test states exactly the environment
// it means instead of inheriting the developer's.
func envFrom(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

// TestDirIsOneRoot pins the whole point of the package: one directory, decided
// by GOLDER_HOME or by $HOME, with XDG_CONFIG_HOME having no say at all. The
// last clause is the one that used to be false — golder's config and
// authorization table lived under ~/.config/golder, so "where does golder keep
// this" had two answers.
func TestDirIsOneRoot(t *testing.T) {
	home := t.TempDir()
	// os.UserHomeDir reads $HOME, so the fallback is exercised for real.
	t.Setenv("HOME", home)

	if got := DirFrom(envFrom(map[string]string{EnvHome: "/custom/state"})); got != "/custom/state" {
		t.Errorf("GOLDER_HOME should win, got %q", got)
	}
	if got := DirFrom(envFrom(map[string]string{})); got != filepath.Join(home, ".golder") {
		t.Errorf("default root = %q, want ~/.golder", got)
	}
	if got := DirFrom(envFrom(map[string]string{"XDG_CONFIG_HOME": "/tmp/xdg"})); got != filepath.Join(home, ".golder") {
		t.Errorf("XDG_CONFIG_HOME must not decide the root, got %q", got)
	}
}

// TestFileFromJoinsUnderTheRoot keeps the constants honest: every name resolves
// under the root, and an unresolvable root yields "" rather than a relative
// path that would land in the user's repository.
func TestFileFromJoinsUnderTheRoot(t *testing.T) {
	env := envFrom(map[string]string{EnvHome: "/custom/state"})
	if got, want := FileFrom(env, GrantsFile), "/custom/state/grants.json"; got != want {
		t.Errorf("FileFrom = %q, want %q", got, want)
	}
	if got, want := FileFrom(env, SessionsDir), "/custom/state/sessions"; got != want {
		t.Errorf("FileFrom(dir) = %q, want %q", got, want)
	}
	if got := FileFrom(envFrom(map[string]string{}), GrantsFile); got != "" && !strings.HasPrefix(got, "/") {
		t.Errorf("an unresolvable root must yield \"\" or an absolute path, got %q", got)
	}
}

// TestMigrateLegacyMovesOnce covers the move a user actually experiences: their
// files are in ~/.config/golder, and after one launch they are in the state root
// with the same contents. The destination winning over the source is the rule
// that keeps a second launch, or a hand-made new file, from being clobbered.
func TestMigrateLegacyMovesOnce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(EnvHome, "") // the move is home-relative only
	legacy := filepath.Join(home, ".config", "golder")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{ConfigFile, GrantsFile} {
		if err := os.WriteFile(filepath.Join(legacy, name), []byte("legacy:"+name), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var moved []string
	MigrateLegacy(os.Getenv, func(msg string) { moved = append(moved, msg) })
	for _, name := range []string{ConfigFile, GrantsFile} {
		got, err := os.ReadFile(filepath.Join(home, ".golder", name))
		if err != nil {
			t.Fatalf("%s not moved: %v", name, err)
		}
		if string(got) != "legacy:"+name {
			t.Errorf("%s content = %q, want it carried over unchanged", name, got)
		}
	}
	if len(moved) < 2 {
		t.Errorf("each move should be announced, got %v", moved)
	}

	// A leftover permissions.toml is NOT moved: it is retired, and only the
	// authorization migration reads it, from where it was written.
	if _, err := os.Stat(filepath.Join(home, ".golder", "permissions.toml")); !os.IsNotExist(err) {
		t.Error("permissions.toml must not be copied into the state root")
	}
}

// TestMigrateLegacyDefersToTheDestination pins the rule that makes the move safe
// to run on every start: a file already at the destination is the live one, and
// the legacy copy is left alone rather than overwriting it.
func TestMigrateLegacyDefersToTheDestination(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(EnvHome, "")
	legacy := filepath.Join(home, ".config", "golder")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, ConfigFile), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".golder"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".golder", ConfigFile), []byte("current"), 0o600); err != nil {
		t.Fatal(err)
	}

	MigrateLegacy(os.Getenv, nil)
	got, err := os.ReadFile(filepath.Join(home, ".golder", ConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "current" {
		t.Errorf("destination = %q, want the live file untouched", got)
	}
}

// TestMigrateLegacySkippedWithGolderHome: the variable names the state root, so
// a home-relative legacy directory has nothing to do with it. Moving files into
// it would be inventing state the user did not point at.
func TestMigrateLegacySkippedWithGolderHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	legacy := filepath.Join(home, ".config", "golder")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, ConfigFile), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	t.Setenv(EnvHome, state)

	MigrateLegacy(os.Getenv, func(msg string) { t.Errorf("nothing should be reported, got %q", msg) })
	if _, err := os.Stat(filepath.Join(state, ConfigFile)); !os.IsNotExist(err) {
		t.Error("GOLDER_HOME is the answer; nothing may be moved into it")
	}
}
