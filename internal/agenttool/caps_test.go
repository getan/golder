package agenttool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/browserctl"
)

// withBrowserSocket points the session at a socket for the duration of a test.
// The environment variable wins over the override, so it is cleared first.
func withBrowserSocket(t *testing.T, path string) {
	t.Helper()
	t.Setenv("KITTY_BROWSE_SOCKET", "")
	browserSocketMu.Lock()
	old := browserSocketOverride
	browserSocketOverride = path
	browserSocketMu.Unlock()
	t.Cleanup(func() {
		browserSocketMu.Lock()
		browserSocketOverride = old
		browserSocketMu.Unlock()
	})
}

func schemaEnum(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var doc struct {
		Properties struct {
			Action struct {
				Enum []string `json:"enum"`
			} `json:"action"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("schema is not JSON: %v", err)
	}
	return doc.Properties.Action.Enum
}

// Before a browser exists the tool advertises open and list, and says how to
// start one; the other actions would only produce the same failure.
func TestBrowserSchemaShrinksWithoutASocket(t *testing.T) {
	withBrowserSocket(t, "")
	tool := NewBrowserTool(t.TempDir(), true)
	got := schemaEnum(t, tool.Schema())
	if len(got) != 3 || got[0] != "open" || got[1] != "list" || got[2] != "attach" {
		t.Fatalf("schema actions = %v, want open, list and attach", got)
	}
	if !strings.Contains(tool.Description(), "No browser is selected") {
		t.Errorf("description = %q, want the start hint", tool.Description())
	}
}

// The socket a preset starts is enough: no environment variable, no restart.
func TestBrowserSchemaGrowsWhenABrowserAppears(t *testing.T) {
	withBrowserSocket(t, "/tmp/work.sock")
	tool := NewBrowserTool(t.TempDir(), true)
	got := schemaEnum(t, tool.Schema())
	if len(got) < 10 {
		t.Fatalf("schema actions = %v, want the control set", got)
	}
	if strings.Contains(tool.Description(), "No browser is running") {
		t.Errorf("description still says no browser: %q", tool.Description())
	}
}

// The registry compiles the wide schema, so a control call still validates
// while the advertised enum is narrowed: the validator must not depend on the
// moment a tool was registered.
func TestBrowserValidationStaysWideWhenAdvertisedNarrow(t *testing.T) {
	withBrowserSocket(t, "")
	tool := NewBrowserTool(t.TempDir(), true)
	reg := NewToolRegistry()
	if err := reg.Register(tool); err != nil {
		t.Fatal(err)
	}
	errs := reg.Validate("browser", json.RawMessage(`{"action":"click","ref":"@e1"}`))
	if len(errs) != 0 {
		t.Fatalf("click did not validate: %+v", errs)
	}
}

// open is the one call that replaces the three-command setup. With a browser
// already running it says so instead of starting a second one.
func TestBrowserOpenIsIdempotentWithASocket(t *testing.T) {
	withBrowserSocket(t, "/tmp/work.sock")
	tool := NewBrowserTool(t.TempDir(), true)
	trustFakeControl(tool.Control)
	tool.discover = func(context.Context) browserDiscovery {
		return browserDiscovery{Instances: []browserInstance{{Socket: "/tmp/work.sock", Selected: true, Info: browserctl.Info{BrowserID: "b1"}}}}
	}
	res, err := tool.Execute(context.Background(), "1",
		json.RawMessage(`{"action":"open"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("open failed: %s", agentcore.ContentToText(res.Content))
	}
	text := agentcore.ContentToText(res.Content)
	if !strings.Contains(text, `"started":false`) || !strings.Contains(text, "/tmp/work.sock") {
		t.Errorf("open = %s, want the existing socket and started=false", text)
	}
}

// Outside a tuios pane there is nowhere to draw a browser, so open explains
// rather than launching something that cannot render.
func TestBrowserOpenOutsideTuiosExplains(t *testing.T) {
	withBrowserSocket(t, "")
	t.Setenv("TUIOS_ENV", "")
	t.Setenv("TUIOS_SOCKET", "")
	t.Setenv("TUIOS_SESSION", "")
	t.Setenv("TUIOS_PANE_ID", "")
	tool := NewBrowserTool(t.TempDir(), true)
	trustFakeControl(tool.Control)
	res, err := tool.Execute(context.Background(), "1",
		json.RawMessage(`{"action":"open","url":"https://example.com"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("open outside a pane must fail")
	}
	if text := agentcore.ContentToText(res.Content); !strings.Contains(text, "tuios pane") {
		t.Errorf("open error = %s, want the tuios explanation", text)
	}
}
