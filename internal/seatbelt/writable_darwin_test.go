//go:build darwin

package seatbelt

// Darwin-only sibling of writable_test.go: it constructs the real
// SeatbeltRunner (defined only on darwin) to pin the generated sandbox
// profile, so the platform-neutral registry tests stay compilable on Linux,
// where the CI runs go vet and go test.
//
// It was previously a runtime.GOOS-guarded test inside writable_test.go;
// the guard skipped at run time but still failed to compile on Linux
// (undefined: SeatbeltRunner), which is exactly what broke CI.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/grants"
	"github.com/getan/golder/internal/grants/grantstest"
	"github.com/getan/golder/internal/permissions"
)

// TestWritableRootsInDarwinProfile pins the macOS policy: the generated
// profile allows writing exactly the granted path (plus the project/tmpdir).
func TestWritableRootsInDarwinProfile(t *testing.T) {
	if !Available() {
		t.Skip("sandbox-exec not on PATH")
	}
	store := grantstest.Install(t)
	_ = store
	granted := t.TempDir()
	if _, err := grants.AddWritableRoot(granted, grants.Session, "test"); err != nil {
		t.Fatal(err)
	}
	canon := permissions.Canonical(granted)
	project := t.TempDir()
	r := &SeatbeltRunner{ProjectDir: project, TmpDir: t.TempDir()}
	argv, cleanup, err := r.SandboxArgv("/bin/bash", "-c", "echo hi", project)
	if err != nil {
		t.Fatalf("SandboxArgv: %v", err)
	}
	defer cleanup()
	data, err := os.ReadFile(argv[2])
	if err != nil {
		t.Fatalf("read profile: %v", err)
	}
	text := string(data)
	writeLine := ""
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "(allow file-write*") {
			writeLine = line
		}
	}
	if writeLine == "" {
		t.Fatalf("profile has no write rule:\n%s", text)
	}
	if !strings.Contains(writeLine, "(subpath \""+canon+"\")") {
		t.Errorf("write rule missing the granted root:\n%s", writeLine)
	}
	if !strings.Contains(text, "(subpath \""+canon+"\")") {
		t.Errorf("profile does not make the granted root readable:\n%s", text)
	}
}

// A parent refusal must not swallow a more specific grant: the table resolves
// most-specific-wins, so the profile has to re-assert the child after the
// parent's deny rule (later rules win).
func TestSpecificGrantIsReassertedAfterParentDeny(t *testing.T) {
	if !Available() {
		t.Skip("sandbox-exec not on PATH")
	}
	grantstest.Install(t)
	parent := t.TempDir()
	child := filepath.Join(parent, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := grants.AddWritableRoot(child, grants.Remembered, "test"); err != nil {
		t.Fatalf("AddWritableRoot: %v", err)
	}
	if _, err := grants.Refuse(parent, grants.ReadWrite, grants.Remembered); err != nil {
		t.Fatalf("Refuse: %v", err)
	}
	if !grants.Allows(child, grants.Write) {
		t.Fatal("the table should allow the more specific child")
	}

	policy := CallPolicy{}
	RefusedPaths(&policy)
	r := &SeatbeltRunner{ProjectDir: t.TempDir(), TmpDir: t.TempDir()}
	argv, cleanup, err := r.SandboxCall([]string{"/bin/bash", "-c", "echo hi"}, "", policy)
	if err != nil {
		t.Fatalf("SandboxCall: %v", err)
	}
	defer cleanup()
	data, err := os.ReadFile(argv[2])
	if err != nil {
		t.Fatalf("read profile: %v", err)
	}

	parentCanon := permissions.Canonical(parent)
	childCanon := permissions.Canonical(child)
	denyRead, denyWrite, allowRead, allowWrite := -1, -1, -1, -1
	for i, line := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(line, "(deny file-read*") && strings.Contains(line, `(subpath "`+parentCanon+`")`):
			denyRead = i
		case strings.HasPrefix(line, "(deny file-write*") && strings.Contains(line, `(subpath "`+parentCanon+`")`):
			denyWrite = i
		case strings.HasPrefix(line, "(allow file-read*") && strings.Contains(line, `(subpath "`+childCanon+`")`):
			allowRead = i
		case strings.HasPrefix(line, "(allow file-write*") && strings.Contains(line, `(subpath "`+childCanon+`")`):
			allowWrite = i
		}
	}
	if denyRead < 0 || allowRead <= denyRead {
		t.Errorf("read: child allow (line %d) must follow the parent deny (line %d)\n%s", allowRead, denyRead, data)
	}
	if denyWrite < 0 || allowWrite <= denyWrite {
		t.Errorf("write: child allow (line %d) must follow the parent deny (line %d)\n%s", allowWrite, denyWrite, data)
	}
}

// /bin/sh reads /private/var/select/sh; the profile must admit it or a nested
// `sh -c` fails with EPERM even though /bin and /usr are readable.
func TestDarwinProfileAdmitsTheShellSelector(t *testing.T) {
	if !Available() {
		t.Skip("sandbox-exec not on PATH")
	}
	r := &SeatbeltRunner{ProjectDir: t.TempDir(), TmpDir: t.TempDir()}
	argv, cleanup, err := r.SandboxArgv("/bin/bash", "-c", "echo hi", "")
	if err != nil {
		t.Fatalf("SandboxArgv: %v", err)
	}
	defer cleanup()
	data, err := os.ReadFile(argv[2])
	if err != nil {
		t.Fatalf("read profile: %v", err)
	}
	if !strings.Contains(string(data), `(subpath "/private/var/select")`) {
		t.Errorf("profile does not admit /private/var/select; nested sh -c will fail:\n%s", data)
	}
}
