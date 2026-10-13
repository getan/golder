package hostcompat

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The live test: the one check that the reports this package sends are actually
// accepted by the host, rather than only well-formed by our own reading of its
// contract. It needs a real host pane, so it is opt-in:
//
//	GOLDER_HOSTCOMPAT_LIVE=1 go test ./internal/hostcompat/ -run TestLive
//
// Run it from inside a tuios window (or a herdr pane) whose manifest is
// installed, and it will report a whole session cycle and then ask the host
// what it saw. Everything it sends is released at the end, so the person's rail
// is left as it was.
func TestLive(t *testing.T) {
	if os.Getenv("GOLDER_HOSTCOMPAT_LIVE") == "" {
		t.Skip("set GOLDER_HOSTCOMPAT_LIVE=1 inside a tuios window or herdr pane to run this")
	}
	host := Detect(os.Getenv)
	if host == "" {
		t.Fatal("not inside a host pane: TUIOS_ENV and HERDR_ENV are both unset")
	}
	t.Logf("running inside a %s pane", host)

	// The real thing: the environment, the binaries, the socket.
	s := OpenSession(Options{ErrLog: os.Stderr})
	if s == nil {
		t.Fatal("OpenSession returned nil inside a host pane")
	}
	if got := s.HostName(); got != host {
		t.Fatalf("HostName = %q, want %q", got, host)
	}

	const sessionID = "20261011-231500-000123"
	s.Start(sessionID, ResumeArgv(sessionID))
	s.TurnStart()
	unblock := s.Block(KindApproval, "approve Bash: rm -rf build")
	unblock()
	s.Meta(map[string]string{MetaModel: "golder-live-test", MetaContext: "42%"})
	s.TurnEnd(false)

	// Give the reporter a moment: it sends on its own goroutine, deliberately,
	// so the agent never waits on the host.
	time.Sleep(300 * time.Millisecond)

	switch host {
	case "tuios":
		assertTuiosSaw(t, sessionID)
	case "herdr":
		assertHerdrSaw(t)
	}

	// Leave the pane as it was.
	s.Close()
}

// assertTuiosSaw asks the tuios daemon what it recorded for this pane.
func assertTuiosSaw(t *testing.T, sessionID string) {
	t.Helper()
	session := os.Getenv("TUIOS_SESSION")
	pane := os.Getenv("TUIOS_PANE_ID")

	// list-windows carries each pane's agent state; the daemon reads reports
	// straight into it.
	out := tuiosJSON(t, "list-windows", "-s", session, "--json")
	if !strings.Contains(out, pane) {
		t.Fatalf("the pane %s is not in list-windows:\n%s", pane, out)
	}
	var windows struct {
		Windows []struct {
			WindowID     string `json:"window_id"`
			AgentState   string `json:"agent_state"`
			AgentHarness string `json:"agent_harness"`
		} `json:"windows"`
	}
	if err := json.Unmarshal([]byte(out), &windows); err != nil {
		t.Fatalf("list-windows --json is not JSON: %v\n%s", err, out)
	}
	var found bool
	for _, w := range windows.Windows {
		if w.WindowID != pane {
			continue
		}
		found = true
		// The state reported at the end of the cycle: a finished turn.
		if w.AgentState != "done" {
			t.Errorf("pane state = %q, want done (the last report was TurnEnd)", w.AgentState)
		}
	}
	if !found {
		t.Errorf("pane %s not found in list-windows", pane)
	}

	// list-agents is the view the rail and the Inbox read.
	agents := tuiosJSON(t, "list-agents", "-s", session, "--json")
	var listed struct {
		Agents []struct {
			Name           string `json:"name"`
			HarnessID      string `json:"harness_id"`
			AgentSessionID string `json:"agent_session_id"`
			State          string `json:"state"`
			Source         string `json:"source"`
			Identity       string `json:"identity"`
		} `json:"agents"`
	}
	if err := json.Unmarshal([]byte(agents), &listed); err != nil {
		t.Fatalf("list-agents --json is not JSON: %v\n%s", err, agents)
	}
	var got bool
	for _, a := range listed.Agents {
		if a.Name != "golder" && a.HarnessID != Harness {
			continue
		}
		got = true
		if a.HarnessID != Harness {
			t.Errorf("harness_id = %q, want %q", a.HarnessID, Harness)
		}
		if a.AgentSessionID != sessionID {
			t.Errorf("agent_session_id = %q, want %q", a.AgentSessionID, sessionID)
		}
		if a.Source != "report" {
			t.Errorf("source = %q, want report (golder reports for itself)", a.Source)
		}
	}
	if !got {
		t.Errorf("list-agents does not show golder:\n%s", agents)
	}
	t.Logf("tuios recorded golder for pane %s", pane)
}

// assertHerdrSaw asks herdr what it recorded for this pane.
func assertHerdrSaw(t *testing.T) {
	t.Helper()
	bin := os.Getenv("HERDR_BIN_PATH")
	pane := os.Getenv("HERDR_PANE_ID")
	out, err := exec.Command(bin, "agent", "list").CombinedOutput()
	if err != nil {
		t.Fatalf("herdr agent list: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), Harness) {
		t.Fatalf("herdr does not list golder for pane %s:\n%s", pane, out)
	}
	t.Logf("herdr recorded golder for pane %s:\n%s", pane, out)
}

// tuiosJSON runs a tuios verb and returns its output, failing on an error.
func tuiosJSON(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("tuios", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("tuios %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
