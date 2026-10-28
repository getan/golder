package extensions

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// Linux's bubblewrap runner returns a nil release callback; macOS returns
// one that removes its generated sandbox profile. Exercise both contracts on
// every platform, without needing a particular OS sandbox to be available.
func TestCommandCleanupSupportsOptionalSandboxRelease(t *testing.T) {
	for _, withRelease := range []bool{false, true} {
		name := "nil release (bubblewrap)"
		if withRelease {
			name = "profile release (seatbelt)"
		}
		t.Run(name, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "extension-home")
			if err := os.Mkdir(home, 0o700); err != nil {
				t.Fatal(err)
			}
			var calls []string
			var release func()
			if withRelease {
				release = func() {
					if _, err := os.Stat(home); err != nil {
						t.Errorf("sandbox must be released before home removal: %v", err)
					}
					calls = append(calls, "sandbox")
				}
			}
			removeHome := func() {
				calls = append(calls, "home")
				if err := os.RemoveAll(home); err != nil {
					t.Error(err)
				}
			}
			// Cleanup also runs when starting an extension fails, so a command
			// without a Process must still release sandbox and temporary state.
			cleanupCommand(&exec.Cmd{}, release, removeHome)
			want := []string{"home"}
			if withRelease {
				want = []string{"sandbox", "home"}
			}
			if !slices.Equal(calls, want) {
				t.Fatalf("cleanup calls = %v, want %v", calls, want)
			}
			if _, err := os.Stat(home); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("extension home leaked: %v", err)
			}
		})
	}
}
