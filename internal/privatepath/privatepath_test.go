package privatepath

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shortTemp keeps runtime paths short enough for the socket limits the callers
// of this package have to respect.
func shortTemp(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "pp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	return dir
}

// EnsureDir creates owner-only, tightens one that is too wide, and refuses one
// that is not a directory at all.
func TestEnsureDirCreatesAndTightens(t *testing.T) {
	base := shortTemp(t)
	fresh := filepath.Join(base, "fresh", "nested")
	if err := EnsureDir(fresh); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	info, err := os.Stat(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != DirMode {
		t.Errorf("%s is mode %04o, want %04o", fresh, perm, DirMode)
	}

	if err := os.Chmod(fresh, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDir(fresh); err != nil {
		t.Fatalf("EnsureDir on a wide directory: %v", err)
	}
	info, err = os.Stat(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != DirMode {
		t.Errorf("%s is mode %04o after EnsureDir, want %04o", fresh, perm, DirMode)
	}

	file := filepath.Join(base, "a-file")
	if err := os.WriteFile(file, []byte("x"), FileMode); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDir(file); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("EnsureDir on a file = %v, want a refusal", err)
	}
}

// A symlink is never followed: the target keeps its mode and gains nothing.
func TestEnsureDirRefusesASymlink(t *testing.T) {
	base := shortTemp(t)
	target := filepath.Join(base, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("this platform has no symlinks: %v", err)
	}
	err := EnsureDir(link)
	if err == nil {
		t.Fatal("EnsureDir tightened a directory through a symlink")
	}
	if !errors.Is(err, ErrSymlink) {
		t.Errorf("err = %v, want it to be ErrSymlink", err)
	}
	info, statErr := os.Lstat(target)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if perm := info.Mode().Perm(); perm != 0o755 {
		t.Errorf("the link's target is now mode %04o, want it untouched at 0755", perm)
	}
}

// Ownership is refused rather than adopted. Only a machine with more than one
// account (or a root test run) can demonstrate it, so the rest of the check runs
// wherever chown is permitted.
func TestEnsureDirRefusesAnotherOwnersDirectory(t *testing.T) {
	base := shortTemp(t)
	dir := filepath.Join(base, "theirs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	other := os.Geteuid() + 1000
	if err := os.Chown(dir, other, other); err != nil {
		t.Skipf("cannot hand a directory to another user here (%v)", err)
	}
	err := EnsureDir(dir)
	if err == nil {
		t.Fatal("EnsureDir adopted another account's directory")
	}
	if !errors.Is(err, ErrForeignOwner) {
		t.Errorf("err = %v, want it to be ErrForeignOwner", err)
	}
	info, statErr := os.Lstat(dir)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if perm := info.Mode().Perm(); perm != 0o755 {
		t.Errorf("the other account's directory is now mode %04o, want it untouched at 0755", perm)
	}
}

// A directory whose mode denies even reading it cannot be opened to be checked,
// and it is still the state directory that has to be repaired: the tightening is
// not skipped just because the mode is worse than 0755.
func TestEnsureDirTightensADirectoryItCannotOpen(t *testing.T) {
	base := shortTemp(t)
	dir := filepath.Join(base, "unreadable")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o100); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if err := EnsureDir(dir); err != nil {
		t.Fatalf("EnsureDir on a 0100 directory: %v", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != DirMode {
		t.Errorf("%s is mode %04o after EnsureDir, want %04o", dir, perm, DirMode)
	}
}

// CheckFile and ReadFile are the read side: regular, ours, and no wider than the
// mode a private file has.
func TestCheckFileAndReadFile(t *testing.T) {
	base := shortTemp(t)
	if err := EnsureDir(base); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(base, "private.json")
	body := []byte(`{"hello":"there"}`)
	if err := os.WriteFile(path, body, FileMode); err != nil {
		t.Fatal(err)
	}
	if err := CheckFile(path, FileMode); err != nil {
		t.Fatalf("CheckFile: %v", err)
	}
	got, err := ReadFile(path, FileMode, 1<<10)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(body) {
		t.Errorf("ReadFile = %q, want %q", got, body)
	}
	if limited, err := ReadFile(path, FileMode, 4); err != nil || string(limited) != `{"he` {
		t.Errorf("ReadFile with a limit = %q, %v", limited, err)
	}

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckFile(path, FileMode); !errors.Is(err, ErrNotPrivate) {
		t.Errorf("CheckFile on a 0644 file = %v, want ErrNotPrivate", err)
	}
	if _, err := ReadFile(path, FileMode, 1<<10); !errors.Is(err, ErrNotPrivate) {
		t.Errorf("ReadFile on a 0644 file = %v, want ErrNotPrivate", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := CheckFile(filepath.Join(base, "missing"), FileMode); err == nil {
		t.Error("CheckFile accepted a file that is not there")
	}
	if err := CheckFile(base, FileMode); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Errorf("CheckFile on a directory = %v, want a refusal", err)
	}
}

// The symlink case on the read side, which is the one that used to hand a
// repository the contents of any private file on the machine.
func TestReadFileRefusesASymlink(t *testing.T) {
	base := shortTemp(t)
	secret := filepath.Join(base, "secret")
	if err := os.WriteFile(secret, []byte("do not read me"), FileMode); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "borrowed.json")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("this platform has no symlinks: %v", err)
	}
	if _, err := ReadFile(link, FileMode, 1<<10); !errors.Is(err, ErrSymlink) {
		t.Errorf("ReadFile through a symlink = %v, want ErrSymlink", err)
	}
}

// RuntimeRoot is per user in a shared temp directory and unsuffixed where the
// runtime directory is already per user. A relative value is ignored.
func TestRuntimeRoot(t *testing.T) {
	env := func(kv map[string]string) func(string) string {
		return func(k string) string { return kv[k] }
	}
	shared := filepath.Join("/tmp/u", RuntimeName())
	cases := []struct {
		name string
		kv   map[string]string
		want string
	}{
		{name: "XDG wins, already per user", kv: map[string]string{"XDG_RUNTIME_DIR": "/run/user/1", "TMPDIR": "/tmp/u"}, want: "/run/user/1/golder"},
		{name: "TMPDIR is scoped", kv: map[string]string{"TMPDIR": "/tmp/u"}, want: shared},
		{name: "TEMP is scoped", kv: map[string]string{"TEMP": "/tmp/u"}, want: shared},
		{name: "relative ignored", kv: map[string]string{"XDG_RUNTIME_DIR": "run", "TMPDIR": "/tmp/u"}, want: shared},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RuntimeRoot(env(tc.kv)); got != tc.want {
				t.Errorf("RuntimeRoot = %q, want %q", got, tc.want)
			}
		})
	}
	if !strings.HasPrefix(RuntimeName(), "golder") {
		t.Errorf("RuntimeName = %q, want a name that says it is golder's", RuntimeName())
	}
	if other := RuntimeRoot(env(map[string]string{})); other == "" {
		t.Error("RuntimeRoot with no environment resolved to nothing")
	}
	// The name must stay a single path component whatever the platform.
	if strings.ContainsAny(RuntimeName()[len("golder"):], `/\`) {
		t.Errorf("RuntimeName = %q, want one path component", RuntimeName())
	}
}

// DirMode and FileMode are the modes every caller relies on.
func TestModesArePrivate(t *testing.T) {
	for name, perm := range map[string]fs.FileMode{"DirMode": DirMode, "FileMode": FileMode} {
		if perm&0o077 != 0 {
			t.Errorf("%s = %04o, and something other than the owner may touch it", name, perm)
		}
	}
}
