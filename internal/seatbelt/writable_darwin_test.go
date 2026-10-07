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
	"strings"
	"testing"
)

// TestWritableRootsInDarwinProfile pins the macOS policy: the generated
// profile allows writing exactly the granted path (plus the project/tmpdir).
func TestWritableRootsInDarwinProfile(t *testing.T) {
	if !Available() {
		t.Skip("sandbox-exec not on PATH")
	}
	defer SetWritableRoots(nil)
	granted := t.TempDir()
	if _, err := AddWritableRoot(granted); err != nil {
		t.Fatal(err)
	}
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
	if !strings.Contains(writeLine, "(subpath \""+granted+"\")") {
		t.Errorf("write rule missing the granted root:\n%s", writeLine)
	}
	if !strings.Contains(text, "(subpath \""+granted+"\")") {
		t.Errorf("profile does not make the granted root readable:\n%s", text)
	}
}
