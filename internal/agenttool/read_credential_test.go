package agenttool

// The read tool refuses credential material even when the resolved path sits
// inside its Root (a workspace rooted in $HOME, or a relative path landing in a
// credential directory there). This complements the judge static floor, which
// only sees the raw argument and cannot resolve relative spellings.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadToolRefusesCredentialPaths(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	// Root at $HOME so the resolved path is exactly the credential location.
	probe := filepath.Join(home, ".golder-credential-probe")
	if err := os.WriteFile(probe, []byte("secret"), 0o600); err != nil {
		t.Skipf("cannot write probe under home: %v", err)
	}
	defer os.Remove(probe)

	tool := &ReadTool{Root: home}
	// .ssh is not writable in a test, so exercise the segment rule with a
	// relative spelling that resolves into home's .ssh directory: the check
	// runs on the resolved path, so a nonexistent file still reports the
	// credential refusal before the stat.
	res, _ := runRead(t, tool, map[string]any{"path": ".ssh/id_rsa"})
	if text := resultText(res); !strings.Contains(text, "credential") {
		t.Errorf("read .ssh/id_rsa should be refused as credential material, got %q", text)
	}

	// An ordinary file under the same root stays readable.
	res, _ = runRead(t, tool, map[string]any{"path": ".golder-credential-probe"})
	if text := resultText(res); !strings.Contains(text, "secret") {
		t.Errorf("ordinary file under home should stay readable, got %q", text)
	}
}
