package agenttool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBrowserProfileWorkspaceOverlapAndHomeAreDistinct(t *testing.T) {
	for _, tc := range []struct {
		name, root, state string
		reject            bool
	}{
		{"default GUI home workspace", "user-home", "", true},
		{"GOLDER_HOME as workspace", "user-home/.golder", "", true},
		{"ordinary project inside home", "user-home/projects/demo", "", false},
		{"custom state inside project", "user-home/projects/demo", "user-home/projects/demo/private-state", true},
		{"explicit state outside home", "user-home", "private-state", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			home := filepath.Join(base, "user-home")
			root := filepath.Join(base, filepath.FromSlash(tc.root))
			if err := os.MkdirAll(root, 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			t.Setenv("GOLDER_HOME", "")
			state := filepath.Join(home, ".golder")
			if tc.state != "" {
				state = filepath.Join(base, filepath.FromSlash(tc.state))
				t.Setenv("GOLDER_HOME", state)
			}
			profile, err := persistentBrowserProfile(root, sharedBrowserProfileName, true)
			if tc.reject {
				if err == nil || profile != "" || !strings.Contains(err.Error(), "Select a narrower project folder") || !strings.Contains(err.Error(), "Do not automatically relocate GOLDER_HOME") {
					t.Fatalf("workspace overlap was accepted or has misleading remediation: profile=%q err=%v", profile, err)
				}
				if _, err := os.Stat(filepath.Join(state, "browser-profiles")); !os.IsNotExist(err) {
					t.Fatalf("rejected browser setup still created profile storage: %v", err)
				}
				return
			}
			if err != nil || profile == "" || insidePath(root, profile) {
				t.Fatalf("non-overlapping profile rejected: profile=%q err=%v", profile, err)
			}
			if !strings.HasPrefix(profile, filepath.Join(state, "browser-profiles")+string(filepath.Separator)) {
				t.Fatalf("profile was silently relocated: %q", profile)
			}
		})
	}
}
