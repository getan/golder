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

func TestDetect(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"plain terminal", map[string]string{}, ""},
		{"tuios", map[string]string{"TUIOS_ENV": "1"}, "tuios"},
		{"herdr", map[string]string{"HERDR_ENV": "1"}, "herdr"},
		// Both set: tuios wins, because a herdr variable leaking into a tuios
		// pane (or vice versa) must not send reports to the wrong host.
		{"both", map[string]string{"TUIOS_ENV": "1", "HERDR_ENV": "1"}, "tuios"},
		// A truthy-but-not-1 value is not the host's contract; both hosts
		// document "1".
		{"TUIOS_ENV=0", map[string]string{"TUIOS_ENV": "0"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Detect(env(tc.env)); got != tc.want {
				t.Errorf("Detect = %q, want %q", got, tc.want)
			}
		})
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
