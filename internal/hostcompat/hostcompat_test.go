package hostcompat

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// recorder is a fake host runner: it records every argv a reporter sends and
// signals on a channel, so a test can wait for the reporter's goroutine without
// sleeping.
type recorder struct {
	mu    sync.Mutex
	calls [][]string
	ch    chan []string
	err   error
}

func newRecorder() *recorder {
	return &recorder{ch: make(chan []string, 16)}
}

func (r *recorder) run(_ context.Context, argv []string) error {
	r.mu.Lock()
	r.calls = append(r.calls, argv)
	r.mu.Unlock()
	r.ch <- argv
	return r.err
}

// next waits for the next call, failing the test rather than hanging.
func (r *recorder) next(t *testing.T) []string {
	t.Helper()
	select {
	case argv := <-r.ch:
		return argv
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a host call")
		return nil
	}
}

// all returns the calls recorded so far.
func (r *recorder) all() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]string(nil), r.calls...)
}

// env builds a Getenv over a literal map.
func env(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

// tuiosPane is the environment of a tuios pane running golder.
func tuiosPane() map[string]string {
	return map[string]string{
		"TUIOS_ENV":      "1",
		"TUIOS_PANE_ID":  "98db8226-1829-468e-89a8-41a2baa0ddab",
		"TUIOS_SESSION":  "work",
		"TUIOS_SOCKET":   "/run/user/1000/tuios/tuios.sock",
		"TUIOS_HOST":     "laptop",
		"TUIOS_PANE_TTY": "/dev/pts/3",
	}
}

// workspacePane is the environment of a pane golder's own workspace started:
// the tuios variables that daemon plants, plus the marker that says whose
// daemon it is.
func workspacePane() map[string]string {
	return map[string]string{
		"GOLDER_WORKSPACE": "1",
		"TUIOS_ENV":        "1",
		"TUIOS_PANE_ID":    "98db8226-1829-468e-89a8-41a2baa0ddab",
		"TUIOS_SESSION":    "work",
		"TUIOS_SOCKET":     "/tmp/golder-workspace-test.sock",
		"TUIOS_PANE_TOKEN": "proof-of-this-pane",
	}
}

// herdrPane is the environment of a herdr pane running golder.
func herdrPane() map[string]string {
	return map[string]string{
		"HERDR_ENV":         "1",
		"HERDR_PANE_ID":     "w1:p2",
		"HERDR_BIN_PATH":    "/usr/local/bin/herdr",
		"HERDR_SOCKET_PATH": "/run/user/1000/herdr.sock",
	}
}

// fakeLookPath answers with a plausible tuios path for any name.
func fakeLookPath(string) (string, error) { return "/usr/local/bin/tuios", nil }

// openFor builds a reporter over a fake runner, with the environment and
// lookpath a test wants.
func openFor(t *testing.T, getenv map[string]string, rec *recorder) *Reporter {
	t.Helper()
	rep := Open(Options{
		Getenv:   env(getenv),
		LookPath: fakeLookPath,
		Run:      rec.run,
		Now:      func() time.Time { return time.Unix(0, 1750000000000000000) },
	})
	if rep == nil {
		t.Fatal("Open returned nil inside a host pane")
	}
	return rep
}

// waitIdle waits for the reporter to drain everything queued.
func waitIdle(t *testing.T, rep *Reporter) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		rep.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reporter did not drain")
	}
}

// TestDetect names the host a report will actually reach: it is the name Open
// opens, Reporter.HostName returns and `host-compat status` prints, so a pane
// whose environment is incomplete says "nowhere" rather than naming a host it
// cannot reach.
func TestDetect(t *testing.T) {
	both := tuiosPane()
	both["HERDR_ENV"], both["HERDR_PANE_ID"], both["HERDR_BIN_PATH"] = "1", "w1:p2", "/bin/herdr"

	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"plain terminal", map[string]string{}, ""},
		{"tuios", tuiosPane(), MuxTuios},
		{"herdr", herdrPane(), MuxHerdr},
		{"golder's own workspace", workspacePane(), MuxGolder},
		// Both set: tuios wins, because a herdr variable leaking into a tuios
		// pane (or vice versa) must not send reports to the wrong host.
		{"both", both, MuxTuios},
		// A truthy-but-not-1 value is not the host's contract; both hosts
		// document "1".
		{"TUIOS_ENV=0", map[string]string{"TUIOS_ENV": "0"}, ""},
		// A host variable without the pane it would report about is not a host
		// this process can report to.
		{"tuios without a pane", map[string]string{"TUIOS_ENV": "1"}, ""},
		{"herdr without a pane", map[string]string{"HERDR_ENV": "1"}, ""},
		{"workspace without a socket", map[string]string{"GOLDER_WORKSPACE": "1", "TUIOS_PANE_ID": "p"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Detect(env(tc.env)); got != tc.want {
				t.Errorf("Detect = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDetectMultiplexer is the detection table a person reads: every terminal
// golder recognises is named, and the ones with no driver report nowhere rather
// than pretending (see OpenIsNilForAMultiplexerWithoutADriver).
func TestDetectMultiplexer(t *testing.T) {
	workspaceInTuios := workspacePane() // a workspace pane carries TUIOS_* too
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"plain terminal", map[string]string{}, MuxNone},
		{"golder workspace", workspacePane(), MuxGolder},
		{"tuios", tuiosPane(), MuxTuios},
		{"herdr", herdrPane(), MuxHerdr},
		// golder's own workspace wins over the tuios variables it also sets:
		// the daemon behind them is golder, and the report goes to it directly.
		{"golder workspace inside a tuios pane", workspaceInTuios, MuxGolder},
		{"tmux", map[string]string{"TMUX": "/tmp/tmux-1000/default,42,0"}, MuxTmux},
		{"zellij", map[string]string{"ZELLIJ": "0"}, MuxZellij},
		{"zellij by session", map[string]string{"ZELLIJ_SESSION_NAME": "work"}, MuxZellij},
		{"screen", map[string]string{"STY": "1234.pts-0.laptop"}, MuxScreen},
		// A tuios pane nested in tmux is a tuios pane.
		{"tuios inside tmux", map[string]string{"TUIOS_ENV": "1", "TMUX": "/tmp/tmux-1000/default,42,0"}, MuxTuios},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Resolve(env(tc.env)).Multiplexer; got != tc.want {
				t.Errorf("Multiplexer = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestWorkspaceMarkerValues: GOLDER_WORKSPACE is golder's own marker, so its
// words are read rather than only "1" - but "0" is not enabled, and neither is
// an empty value. A wrapper that exports GOLDER_WORKSPACE=0 must not send
// reports to a workspace that is not there.
func TestWorkspaceMarkerValues(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"1", true}, {"true", true}, {"TRUE", true}, {"yes", true}, {"on", true}, {" 1 ", true},
		{"", false}, {"0", false}, {"false", false}, {"no", false}, {"off", false}, {"banana", false},
	} {
		kv := workspacePane()
		kv["GOLDER_WORKSPACE"] = tc.value
		got := Resolve(env(kv)).Multiplexer == MuxGolder
		if got != tc.want {
			t.Errorf("GOLDER_WORKSPACE=%q enabled = %v, want %v", tc.value, got, tc.want)
		}
	}
}

// TestResolveIsTheOneDecision: the host Detect names, the host Open opens and
// the host a person is told about are one answer, which is what keeps the rail
// and `host-compat status` from disagreeing.
func TestResolveIsTheOneDecision(t *testing.T) {
	for _, kv := range []map[string]string{
		{},
		tuiosPane(),
		herdrPane(),
		workspacePane(),
		{"GOLDER_WORKSPACE": "1", "TUIOS_PANE_ID": "p"},
		{"TMUX": "/tmp/tmux-1000/default,42,0"},
		{"TUIOS_ENV": "1", "TUIOS_PANE_ID": "pane-1", EnvHostReport: "off"},
	} {
		res := Resolve(env(kv))
		if got := Detect(env(kv)); got != res.Host {
			t.Errorf("Detect = %q, Resolve().Host = %q: they must agree", got, res.Host)
		}
		rep := Open(Options{Getenv: env(kv), LookPath: fakeLookPath, Run: newRecorder().run})
		if got := rep.HostName(); got != res.Host {
			t.Errorf("Open inside %v opened %q, Resolve says %q", kv, got, res.Host)
		}
		if res.Host != "" && len(res.Missing) > 0 {
			t.Errorf("Resolve reports host %q but also missing %v", res.Host, res.Missing)
		}
		if res.Host == "" && res.Multiplexer != "" && len(requirements[res.Multiplexer]) > 0 && len(res.Missing) == 0 && !res.Off {
			t.Errorf("host %q: nothing reported and nothing missing", res.Multiplexer)
		}
	}
}

// TestOpenIsNilForAMultiplexerWithoutADriver: recognising tmux is not the same
// as reporting to it. Nothing is sent, and status is what tells the person.
func TestOpenIsNilForAMultiplexerWithoutADriver(t *testing.T) {
	for name, kv := range map[string]map[string]string{
		"tmux":   {"TMUX": "/tmp/tmux-1000/default,42,0"},
		"zellij": {"ZELLIJ": "0"},
		"screen": {"STY": "1234.pts-0.laptop"},
	} {
		rec := newRecorder()
		if rep := Open(Options{Getenv: env(kv), LookPath: fakeLookPath, Run: rec.run}); rep != nil {
			t.Errorf("%s: Open opened %q, want nil", name, rep.HostName())
		}
		if calls := rec.all(); len(calls) != 0 {
			t.Errorf("%s: %d calls were sent to a multiplexer with no driver: %v", name, len(calls), calls)
		}
		res := Resolve(env(kv))
		if res.Multiplexer == "" {
			t.Errorf("%s: the multiplexer should still be named for status", name)
		}
		if res.Host != "" {
			t.Errorf("%s: Resolve.Host = %q, want empty", name, res.Host)
		}
	}
}

func TestOpenIsNilOutsideAHostPane(t *testing.T) {
	if rep := Open(Options{Getenv: env(map[string]string{}), LookPath: fakeLookPath}); rep != nil {
		t.Fatalf("Open outside a host pane = %v, want nil", rep)
	}
	// And a nil reporter is a working no-op, so no caller needs a guard.
	var rep *Reporter
	rep.State(StateWorking, KindNone, "x")
	rep.Session("id", nil)
	rep.Meta(map[string]string{"model": "m"})
	rep.Release()
	rep.Close()
}

func TestEscapeHatch(t *testing.T) {
	rec := newRecorder()
	// off: no reporter at all, even inside a pane.
	if rep := Open(Options{
		Getenv:   env(map[string]string{"TUIOS_ENV": "1", "TUIOS_PANE_ID": "p", EnvHostReport: "off"}),
		LookPath: fakeLookPath,
		Run:      rec.run,
	}); rep != nil {
		t.Errorf("GOLDER_HOST_REPORT=off still opened a reporter")
	}

	// Forced: a host is used even when the pane's environment says nothing, so
	// a person can test a host from inside another one.
	rep := Open(Options{
		Getenv: env(map[string]string{
			"HERDR_ENV": "1", "HERDR_PANE_ID": "w1:p2", "HERDR_BIN_PATH": "/bin/herdr",
			EnvHostReport: "tuios", "TUIOS_PANE_ID": "pane-1",
		}),
		LookPath: fakeLookPath,
		Run:      rec.run,
	})
	if rep == nil {
		t.Fatal("GOLDER_HOST_REPORT=tuios did not open a reporter")
	}
	if got := rep.HostName(); got != "tuios" {
		t.Errorf("HostName = %q, want tuios", got)
	}
}

func TestOneLine(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"approve Bash: go test ./...", "approve Bash: go test ./..."},
		{"two\nlines", "two lines"},
		{"  leading and trailing  ", "leading and trailing"},
		{"tab\there", "tab here"},
		{"cr\rhere", "cr here"},
		{"many   spaces", "many spaces"},
	}
	for _, tc := range cases {
		if got := oneLine(tc.in); got != tc.want {
			t.Errorf("oneLine(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	long := strings.Repeat("x", 500)
	got := oneLine(long)
	if n := len([]rune(got)); n != 201 {
		t.Errorf("oneLine(500 x) has %d runes, want 200 plus the ellipsis", n)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("a cut line should end with an ellipsis, got %q", got[len(got)-3:])
	}
}

func TestValidSessionID(t *testing.T) {
	// A golder session id, as session.NewID builds it.
	if !ValidSessionID("20261011-231500-000123") {
		t.Error("a golder session id should be valid")
	}
	for _, bad := range []string{
		"",
		"-leading-dash",          // a shell and both hosts read it as a flag
		"has space",              // would split into two arguments
		"quote'inside",           // herdr refuses an apostrophe
		"new\nline",              // would break the line the host reads
		"semi;colon",             // a shell would run what follows
		strings.Repeat("x", 129), // past MaxResumeSessionID
	} {
		if ValidSessionID(bad) {
			t.Errorf("ValidSessionID(%q) = true, want false", bad)
		}
	}
}

func TestResumeArgv(t *testing.T) {
	got := ResumeArgv("20261011-231500-000123")
	want := []string{"golder", "--resume", "20261011-231500-000123"}
	if len(got) != len(want) {
		t.Fatalf("ResumeArgv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ResumeArgv = %v, want %v", got, want)
		}
	}
	if ResumeArgv("bad id") != nil {
		t.Error("ResumeArgv of an unsafe id should be nil, never a typed command")
	}
}

func TestContextMeta(t *testing.T) {
	if got := ContextMeta(0, 0); got != nil {
		t.Errorf("ContextMeta with no window = %v, want nil", got)
	}
	if got := ContextMeta(50000, 100000); got[MetaContext] != "50%" {
		t.Errorf("ContextMeta = %v, want 50%%", got)
	}
	// Out-of-range readings are clamped rather than shown as nonsense.
	if got := ContextMeta(200000, 100000); got[MetaContext] != "100%" {
		t.Errorf("ContextMeta over 100%% = %v", got)
	}
}
