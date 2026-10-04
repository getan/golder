package seatbelt

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestModeFromEnv(t *testing.T) {
	t.Setenv("GOLDER_SANDBOX", "")
	if ModeFromEnv() != ModeAuto {
		t.Fatal("empty GOLDER_SANDBOX should default to auto")
	}
	t.Setenv("GOLDER_SANDBOX", "enforce")
	if ModeFromEnv() != ModeEnforce {
		t.Fatal("GOLDER_SANDBOX=enforce should parse")
	}
	t.Setenv("GOLDER_SANDBOX", "off")
	if ModeFromEnv() != ModeOff {
		t.Fatal("GOLDER_SANDBOX=off should parse")
	}
}

func TestSeatbeltDarwinProfile(t *testing.T) {
	if runtime.GOOS != "darwin" {
		// Linux has its own runner (bubblewrap); availability is machine-
		// dependent there. Only the stub platforms must report unavailable.
		if runtime.GOOS != "linux" {
			if Available() {
				t.Fatal("Available must be false on platforms without a sandbox runner")
			}
			if New(t.TempDir()) != nil {
				t.Fatal("New must return nil on platforms without a sandbox runner")
			}
		}
		t.Skip("profile assertions are darwin-only")
	}
	if !Available() {
		t.Skip("sandbox-exec not on PATH")
	}
	project := t.TempDir()
	r := New(project)
	if r == nil {
		t.Fatal("New returned nil on darwin with sandbox-exec present")
	}
	argv, cleanup, err := r.SandboxArgv("/bin/bash", "-c", "echo hi", project)
	if err != nil {
		t.Fatalf("SandboxArgv: %v", err)
	}
	defer cleanup()
	if len(argv) != 6 || argv[0] != "sandbox-exec" || argv[1] != "-f" {
		t.Fatalf("argv = %v, want sandbox-exec -f <profile> <shell> -c <cmd>", argv)
	}
	profile := argv[2]
	data, err := os.ReadFile(profile)
	if err != nil {
		t.Fatalf("read profile: %v", err)
	}
	text := string(data)
	// The profile is a whitelist: the project, the system runtime and the git
	// files are readable; credential material under $HOME is excluded by
	// construction rather than by name, so it must NOT appear as an allow.
	for _, want := range []string{`(deny default)`, project, "/usr", "(deny file-write* (regex #\".*trust\\.json$\"))"} {
		if !strings.Contains(text, want) {
			t.Errorf("profile missing %q:\n%s", want, text)
		}
	}
	// No blanket read allow, and no allow naming $HOME.
	if strings.Contains(text, `(allow file-read* (subpath "/"))`) {
		t.Errorf("profile must not grant blanket reads:\n%s", text)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		for _, banned := range []string{".ssh", ".gnupg"} {
			if strings.Contains(text, "(subpath \""+home+"/"+banned+"\")") {
				t.Errorf("profile must not allow %s:\n%s", banned, text)
			}
		}
	}
	cleanup()
	if _, err := os.Stat(profile); !os.IsNotExist(err) {
		t.Error("cleanup must remove the generated profile")
	}
}
