package hostcompat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// configEnv is a Getenv over a temporary config home, so an install in a test
// never touches the person's real ~/.config.
func configEnv(t *testing.T) (getenv func(string) string, home string) {
	t.Helper()
	home = t.TempDir()
	return env(map[string]string{"XDG_CONFIG_HOME": home}), home
}

// TestManifestPathFollowsTuios is the one place where a mistake silently
// installs a manifest tuios never reads.
func TestManifestPathFollowsTuios(t *testing.T) {
	getenv, home := configEnv(t)
	want := filepath.Join(home, "tuios", "harnesses", "golder.toml")
	if got := ManifestPath(getenv); got != want {
		t.Errorf("ManifestPath = %q, want %q", got, want)
	}

	// TUIOS_HARNESS_DIR wins, exactly as tuios's own UserDir resolves it.
	override := t.TempDir()
	got := ManifestPath(env(map[string]string{
		"XDG_CONFIG_HOME":   home,
		"TUIOS_HARNESS_DIR": override,
	}))
	if want := filepath.Join(override, "golder.toml"); got != want {
		t.Errorf("with TUIOS_HARNESS_DIR, ManifestPath = %q, want %q", got, want)
	}
}

// TestInstallAndStatus drives the whole cycle: absent, install, current,
// idempotent, uninstall.
func TestInstallAndStatus(t *testing.T) {
	getenv, _ := configEnv(t)

	state, path, err := ManifestStatus(getenv)
	if err != nil {
		t.Fatal(err)
	}
	if state != ManifestAbsent {
		t.Fatalf("before install, state = %v, want absent", state)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("something is already at %s", path)
	}

	res, err := Install(getenv)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed {
		t.Error("the first install should report a change")
	}

	state, _, err = ManifestStatus(getenv)
	if err != nil {
		t.Fatal(err)
	}
	if state != ManifestCurrent {
		t.Errorf("after install, state = %v, want current", state)
	}

	// Installing again is the common case (a person running it twice, an
	// upgrade script), so it must not rewrite the file or report a change.
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	res, err = Install(getenv)
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed {
		t.Error("a second install should not report a change")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("a second install rewrote the file")
	}

	_, removed, err := Uninstall(getenv)
	if err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Error("uninstall should have removed golder's manifest")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the manifest is still there after uninstall: %v", err)
	}
}

// TestInstallRefusesAForeignManifest: a person's own golder.toml is theirs.
// tuios documents editing these by hand, so golder must never overwrite one.
func TestInstallRefusesAForeignManifest(t *testing.T) {
	getenv, _ := configEnv(t)
	path := ManifestPath(getenv)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	const mine = "schema_version = 1\nid = \"golder\"\n# my own rules\n"
	if err := os.WriteFile(path, []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}

	state, _, err := ManifestStatus(getenv)
	if err != nil {
		t.Fatal(err)
	}
	if state != ManifestForeign {
		t.Fatalf("state = %v, want foreign (no golder marker)", state)
	}
	if _, err := Install(getenv); err == nil {
		t.Error("install should refuse to overwrite a manifest golder did not write")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != mine {
		t.Error("install modified a file that is not golder's")
	}
	// Uninstall leaves it alone too.
	if _, removed, err := Uninstall(getenv); err != nil || removed {
		t.Errorf("uninstall of a foreign manifest = removed %v, err %v; want false, nil", removed, err)
	}
	if got, _ := os.ReadFile(path); string(got) != mine {
		t.Error("uninstall removed a file that is not golder's")
	}
}

// TestManifestVersionMarker: the marker is how an install made by an older
// golder is told from a current one, and how a person tells golder's file from
// their own.
func TestManifestVersionMarker(t *testing.T) {
	src := ManifestSource()
	if src == "" {
		t.Fatal("the manifest asset did not embed")
	}
	version, ok := manifestVersion(src)
	if !ok {
		t.Fatalf("the shipped manifest has no %q marker:\n%s", manifestMarker, firstLines(src, 3))
	}
	if version != ManifestVersion {
		t.Errorf("shipped manifest says version %d, ManifestVersion is %d", version, ManifestVersion)
	}

	// An older copy reads as outdated, which is what makes an upgrade visible.
	if got, ok := manifestVersion("# golder-manifest-version: 0\n"); !ok || got != 0 {
		t.Errorf("an older marker = %d, %v; want 0, true", got, ok)
	}
	// Anything else is not golder's.
	if _, ok := manifestVersion("schema_version = 1\nid = \"golder\"\n"); ok {
		t.Error("a file with no marker should not read as golder's")
	}
}

// TestShippedManifestIsUsable checks the asset the way tuios will: the fields
// tuios requires for a loadable manifest, and the ones this package's reports
// depend on. It is not a substitute for `tuios doctor agents` against the real
// daemon — it catches a typo before that.
func TestShippedManifestIsUsable(t *testing.T) {
	src := ManifestSource()
	for _, want := range []string{
		"schema_version = 1",
		`id             = "golder"`,
		`comm  = ["golder"]`,
		`argv0 = ["golder"]`,
		// The screens rules the Inbox needs to press a key for the person.
		"Approve once",
		"Approve for this session",
		"Do you trust this folder?",
		// The two prompts that came second: a read outside the workspace, and
		// the trust question asked mid-run.
		"Allow this read once",
		"untrusted folder",
		"Allow all tools in this folder",
		"[screen.rule.answers]",
		// And the resume command a restored pane is reopened with.
		`argv   = ["golder", "--resume", "{session_id}"]`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("the shipped manifest does not mention %q", want)
		}
	}

	// The answers block must bind its keys to option labels rather than to
	// digits: golder's dialog is not a numbered menu, so an answer keyed by
	// position would press whatever happens to be there.
	if strings.Contains(src, "option =") {
		t.Error("golder's dialogs have no numbered options, so no answer may bind to one")
	}
}

// TestInstallWritesThroughASymlink: a dotfile-managed config leaves a symlink
// where the manifest goes, usually before the target exists. The file the link
// names is what gets written; the link must survive as a link, or the person's
// dotfiles setup quietly loses control of the file.
func TestInstallWritesThroughASymlink(t *testing.T) {
	getenv, home := configEnv(t)
	target := filepath.Join(home, "dotfiles", "tuios", "golder.toml")
	link := ManifestPath(getenv)
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	// Dangling: the dotfiles repository has the entry, the file is not there
	// yet — the state a fresh install finds.
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}

	if _, err := Install(getenv); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("install replaced the symlink with a plain file")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("install did not create the file the link names: %v", err)
	}
	if _, ok := manifestVersion(string(data)); !ok {
		t.Error("install wrote something other than golder's manifest")
	}

	// Installing again sees golder's manifest THROUGH the link, so it is
	// current and needs no rewrite.
	res, err := Install(getenv)
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed {
		t.Error("a second install through the link should be a no-op")
	}
}

// TestInstallRefusesALinkToAForeignFile: a link is not a licence to overwrite
// what it points at. Reading through it finds a file golder did not write, and
// that is where this stops.
func TestInstallRefusesALinkToAForeignFile(t *testing.T) {
	getenv, home := configEnv(t)
	target := filepath.Join(home, "dotfiles", "golder.toml")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	const mine = "# my own manifest\nschema_version = 1\nid = \"golder\"\n"
	if err := os.WriteFile(target, []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}
	link := ManifestPath(getenv)
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}

	if _, err := Install(getenv); err == nil {
		t.Error("install should refuse a link to a file golder did not write")
	}
	if got, _ := os.ReadFile(target); string(got) != mine {
		t.Error("install overwrote the file behind the link")
	}
}

// firstLines is a test failure's context: the head of the file that failed.
func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
