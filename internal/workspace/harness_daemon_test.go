//go:build darwin || linux

package workspace

// The manifest this workspace installs is read by the daemon golder embeds, and
// that daemon validates a manifest as one file: a [resume] block it refuses does
// not cost the resume block alone, it costs the detection, the screen rules and
// everything else the file declares, because the file never becomes a harness at
// all.
//
// So these tests ask the real daemon the two things a TOML decode cannot answer.
// Does it know a harness called golder (a manifest that failed to load is not in
// the registry, and the verb refuses an unknown harness by name, listing what it
// does know)? And does it hold the screen rules the shipped manifest declares
// (explain-agent-screen reports every rule it would run and names the file those
// rules came from)?
//
// The first executable below is a path tuios can type into a shell unquoted. The
// second is not, and it is the case the review found: a workspace built or
// installed under a directory with a space in it used to lose its whole manifest,
// screen rules included, for a resume command nobody had asked for.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/getan/tuios-mini/mini"
	"github.com/getan/tuios-mini/mini/control"

	"github.com/getan/golder/internal/hostcompat"
)

// TestDaemonLoadsTheHarnessThisWorkspaceInstalls is the boundary the review
// question was about: whatever path this workspace's own golder is installed
// under, the daemon must end up with a harness called golder carrying the
// shipped screen rules.
func TestDaemonLoadsTheHarnessThisWorkspaceInstalls(t *testing.T) {
	ruleCount := shippedScreenRuleCount(t)
	for _, tc := range []struct {
		name       string
		executable string
	}{
		{name: "a path tuios can type", executable: "/opt/golder/bin/golder-1.5.0"},
		{name: "a path tuios cannot type", executable: "/opt/My Golder/bin/golder"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, err := (&Backend{Executable: tc.executable}).writeHarness(filepath.Join(t.TempDir(), "state"))
			if err != nil {
				t.Fatalf("write this workspace's harness: %v", err)
			}
			out := explainGolderHarness(t, dir)

			if want := filepath.Join(dir, "golder.toml"); out.manifestSource != want {
				t.Fatalf("the daemon's golder rules come from %q, want %q", out.manifestSource, want)
			}
			if !out.enabled {
				t.Fatal("the daemon loaded golder's manifest with its screen rules disabled")
			}
			if out.rules != ruleCount {
				t.Fatalf("the daemon holds %d of golder's %d screen rules", out.rules, ruleCount)
			}
		})
	}
}

// golderScreen is what explain-agent-screen said about the harness golder owns.
type golderScreen struct {
	manifestSource string
	enabled        bool
	rules          int
}

// explainGolderHarness starts the daemon golder embeds over one harness
// directory, then asks it to explain the pane it started with the rules of a
// harness named golder. An unknown harness is a verb error naming what the
// registry does know, which is exactly what a manifest that failed to load looks
// like from outside.
func explainGolderHarness(t *testing.T, harnessDir string) golderScreen {
	t.Helper()
	root := tempRoot(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	srv, err := mini.Start(ctx, mini.Config{
		SocketPath: filepath.Join(root, "d.sock"),
		Session:    "work",
		Shell:      "/bin/sh",
		Cwd:        root,
		Width:      100,
		Height:     30,
		Version:    "harness-test",
		StateDir:   filepath.Join(root, "state"),
		LogFile:    filepath.Join(root, "daemon.log"),
		HarnessDir: harnessDir,
	})
	if err != nil {
		t.Fatalf("start the daemon golder embeds: %v", err)
	}
	t.Cleanup(srv.Stop)

	ctl, err := control.Dial(ctx, srv.Socket())
	if err != nil {
		t.Fatalf("dial the daemon: %v", err)
	}
	t.Cleanup(func() { _ = ctl.Close() })

	var windows struct {
		Windows []struct {
			WindowID string `json:"window_id"`
		} `json:"windows"`
	}
	raw, err := ctl.Call(ctx, "list-windows", map[string]any{"session": srv.Session()})
	if err != nil {
		t.Fatalf("list the daemon's panes: %v", err)
	}
	if err := json.Unmarshal(raw, &windows); err != nil {
		t.Fatalf("decode the pane list: %v", err)
	}
	if len(windows.Windows) == 0 {
		t.Fatal("the daemon started no pane to explain")
	}

	raw, err = ctl.Call(ctx, "explain-agent-screen", map[string]any{
		"session": srv.Session(), "window": windows.Windows[0].WindowID, "harness": "golder",
	})
	if err != nil {
		t.Fatalf("the daemon does not know the harness %s declares, so it did not load it: %v",
			filepath.Join(harnessDir, "golder.toml"), err)
	}
	var out struct {
		ManifestSource string            `json:"manifest_source"`
		Enabled        bool              `json:"enabled"`
		Rules          []json.RawMessage `json:"rules"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode the screen explanation: %v", err)
	}
	return golderScreen{manifestSource: out.ManifestSource, enabled: out.Enabled, rules: len(out.Rules)}
}

// shippedScreenRuleCount is how many screen rules the manifest golder ships
// declares. The daemon reporting that many back is the screen block surviving
// whatever the resume block had to say about it.
func shippedScreenRuleCount(t *testing.T) int {
	t.Helper()
	var doc struct {
		Screen struct {
			Rule []map[string]any `toml:"rule"`
		} `toml:"screen"`
	}
	if _, err := toml.Decode(hostcompat.ManifestSource(), &doc); err != nil {
		t.Fatalf("decode the bundled golder harness: %v", err)
	}
	if len(doc.Screen.Rule) == 0 {
		t.Fatal("the bundled golder harness declares no screen rules, so this test would prove nothing")
	}
	return len(doc.Screen.Rule)
}
