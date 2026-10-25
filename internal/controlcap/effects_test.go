package controlcap

import (
	"encoding/json"
	"testing"

	"github.com/kballard/go-shellquote"
)

func TestCommandsPreserveArgv(t *testing.T) {
	argv := []string{"sh", "-c", "echo 'a b'; cat /outside/file"}
	raw, _ := json.Marshal(map[string]any{"action": "new", "argv": argv})
	cmds := Commands("tuios", raw)
	if len(cmds) != 1 {
		t.Fatal(cmds)
	}
	got, err := shellquote.Split(cmds[0])
	if err != nil || len(got) != len(argv) {
		t.Fatal(got, err)
	}
	for i := range argv {
		if got[i] != argv[i] {
			t.Fatalf("argv boundary lost: %v", got)
		}
	}
}

func TestAllBrowserURLEntriesRejectActiveAndLocalSchemes(t *testing.T) {
	for _, raw := range []string{"file:///etc/passwd", " File:///etc/passwd", "javascript:1", "data:text/html,x", "chrome://settings", "https://user:secret@example.com", "//example.com"} {
		if BrowserURL(raw) == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	for _, raw := range []string{"", "about:blank", "http://localhost:3000", "https://example.com"} {
		if err := BrowserURL(raw); err != nil {
			t.Errorf("%s: %v", raw, err)
		}
	}
}
