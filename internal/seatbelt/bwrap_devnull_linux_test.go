//go:build linux

package seatbelt

import (
	"os/exec"
	"testing"
)

func TestBwrapDevNullRedirectEndToEnd(t *testing.T) {
	if !Available() {
		t.Skip("bubblewrap not available or usable")
	}
	r := &BwrapRunner{ProjectDir: t.TempDir(), TmpDir: t.TempDir()}
	for _, policy := range []CallPolicy{{}, {WriteFiles: []string{"/dev/null"}}} {
		argv, _, err := r.SandboxCall([]string{"/bin/sh", "-c", `printf hi >/dev/null && ls does-not-exist 2>/dev/null || echo "no partial dir"`}, "", policy)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Dir = r.ProjectDir
		out, err := cmd.CombinedOutput()
		if err != nil || string(out) != "no partial dir\n" {
			t.Fatalf("null redirects with policy %+v: %v, output %q", policy, err, out)
		}
	}
}
