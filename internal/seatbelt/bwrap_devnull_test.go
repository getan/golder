package seatbelt

import (
	"os"
	"path/filepath"
	"testing"
)

// An old approval record or a direct caller can still pass an exact null-device
// grant. It must neither fail the regular-file check nor bind the host device.
func TestBwrapExactWriteDevNull(t *testing.T) {
	r := &BwrapRunner{ProjectDir: t.TempDir(), TmpDir: t.TempDir()}
	file := filepath.Join(r.ProjectDir, "output")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, readOnly := range []bool{false, true} {
		argv, _, err := r.SandboxCall([]string{"sh", "-c", "echo hi 2>/dev/null"}, "", CallPolicy{
			WriteFiles: []string{"/dev/null", file}, ReadOnlyFilesystem: readOnly,
		})
		if err != nil {
			t.Fatalf("readOnly=%v: %v", readOnly, err)
		}
		if indexOfSeq(argv, "--dev", "/dev") < 0 {
			t.Fatal("sandbox must supply its own /dev")
		}
		for _, flag := range []string{"--bind", "--bind-try", "--ro-bind"} {
			if indexOfSeq(argv, flag, "/dev/null", "/dev/null") >= 0 {
				t.Fatalf("must not bind host /dev/null: %v", argv)
			}
		}
		if !readOnly && indexOfSeq(argv, "--bind", file, file) < 0 {
			t.Fatal("a null grant must not suppress other exact file grants")
		}
	}
}

func TestBwrapExactWriteStillRejectsNonRegularFiles(t *testing.T) {
	r := &BwrapRunner{ProjectDir: t.TempDir(), TmpDir: t.TempDir()}
	paths := []string{t.TempDir(), filepath.Join(t.TempDir(), "missing")}
	if info, err := os.Stat("/dev/zero"); err == nil && !info.Mode().IsRegular() {
		paths = append(paths, "/dev/zero")
	}
	for _, path := range paths {
		if _, _, err := r.SandboxCall([]string{"true"}, "", CallPolicy{WriteFiles: []string{path}}); err == nil {
			t.Errorf("exact write %q must not broaden to a directory or device grant", path)
		}
	}
}
