package agenttool

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTuiosScriptPreservesUmaskAndApplicationVariables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app-file")
	p, err := newTuiosScript(`printf '%s:%s' "$n" "$d" > ` + quoteControlArg(path))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", "umask 022; "+p.receiver)
	cmd.Env = append(os.Environ(), "n=application", "d=exported")
	cmd.Stdin = strings.NewReader(p.frames)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "application:exported" {
		t.Fatalf("application env clobbered: %q %v", data, err)
	}
	stat, err := os.Stat(path)
	if err != nil || stat.Mode().Perm() != 0644 {
		t.Fatalf("receiver's private umask leaked into user script: %v %v", stat, err)
	}
}
