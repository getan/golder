package workspacecmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/golderhome"
)

// resolved is the Options one invocation produces, or the error it refused with.
func resolved(t *testing.T, e *testEnv, args ...string) (Options, error) {
	t.Helper()
	p, err := parse(args)
	if err != nil {
		return Options{}, err
	}
	return resolve(p, e.deps())
}

// mustResolve is resolved for the cases that must work.
func mustResolve(t *testing.T, e *testEnv, args ...string) Options {
	t.Helper()
	opts, err := resolved(t, e, args...)
	if err != nil {
		t.Fatalf("resolve(%q): %v", args, err)
	}
	return opts
}

// TestProjectIdentityIsStable pins the contract every other path depends on:
// the same project always resolves to the same identity, config, state, log,
// session and socket.
func TestProjectIdentityIsStable(t *testing.T) {
	e := newTestEnv(t)
	first := mustResolve(t, e)
	second := mustResolve(t, e)
	if first.Paths != second.Paths || first.Session != second.Session || first.Socket != second.Socket {
		t.Errorf("the same project resolved differently:\nfirst: %+v\nsecond: %+v", first.Paths, second.Paths)
	}
	if len(first.ProjectID) != 12 {
		t.Errorf("project id = %q, want 12 hex characters", first.ProjectID)
	}
	if want := filepath.Join(e.home, workspaceDirName, first.ProjectID); first.ProjectDir != want {
		t.Errorf("project dir = %q, want %q", first.ProjectDir, want)
	}
}

// TestTwoProjectsNeverShareAnything is the collision the identity exists to
// prevent: two checkouts that are both called "project" — the common case for
// anyone with two clones of one repository — must not share a session name, a
// state directory or a log, or one project's history would appear in the other.
func TestTwoProjectsNeverShareAnything(t *testing.T) {
	e := newTestEnv(t)
	other := filepath.Join(e.base, "elsewhere", "project")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}

	one := mustResolve(t, e)
	two := mustResolve(t, e, "--cwd", other)

	if one.ProjectID == two.ProjectID {
		t.Errorf("two projects share the identity %q", one.ProjectID)
	}
	for name, pair := range map[string][2]string{
		"project dir": {one.ProjectDir, two.ProjectDir},
		"state dir":   {one.StateDir, two.StateDir},
		"config":      {one.ConfigPath, two.ConfigPath},
		"log":         {one.LogFile, two.LogFile},
		"socket":      {one.Socket, two.Socket},
		"session":     {one.Session, two.Session},
	} {
		if pair[0] == pair[1] {
			t.Errorf("two projects share a %s: %q", name, pair[0])
		}
	}
}

// TestProjectIdentityFollowsSymlinks: reaching a project through a link is the
// same project, so it gets the same identity — otherwise the state directory
// would depend on how the person spelled the path.
func TestProjectIdentityFollowsSymlinks(t *testing.T) {
	e := newTestEnv(t)
	link := filepath.Join(e.base, "link")
	if err := os.Symlink(e.project, link); err != nil {
		t.Skipf("this platform has no symlinks: %v", err)
	}

	direct := mustResolve(t, e)
	through := mustResolve(t, e, "--cwd", link)
	if direct.Project != through.Project {
		t.Errorf("project = %q through a link and %q directly, want one identity", through.Project, direct.Project)
	}
	if direct.ProjectID != through.ProjectID || direct.StateDir != through.StateDir {
		t.Errorf("a link resolved to %q/%q, want %q/%q", through.ProjectID, through.StateDir, direct.ProjectID, direct.StateDir)
	}
}

// TestGolderHomeDecidesTheRoot: the durable paths follow golderhome's one root.
// $GOLDER_HOME is used verbatim, XDG_CONFIG_HOME has no say at all. This is the
// "not a separate XDG state tree" rule.
func TestGolderHomeDecidesTheRoot(t *testing.T) {
	e := newTestEnv(t)
	override := filepath.Join(e.base, "custom-state")
	e.set(golderhome.EnvHome, override)

	got := mustResolve(t, e)
	if got.Home != override {
		t.Fatalf("home = %q, want %q", got.Home, override)
	}
	for name, path := range map[string]string{
		"config": got.ConfigPath,
		"state":  got.StateDir,
		"log":    got.LogFile,
	} {
		if !strings.HasPrefix(path, override) {
			t.Errorf("%s = %q, want it under GOLDER_HOME=%q", name, path, override)
		}
	}

	// A second XDG tree must not appear: XDG_CONFIG_HOME is where golder used
	// to keep half of its state, and it is not consulted any more.
	xdg := filepath.Join(e.base, "xdg")
	e.set("XDG_CONFIG_HOME", xdg)
	again := mustResolve(t, e)
	for _, path := range []string{again.ConfigPath, again.StateDir, again.LogFile} {
		if strings.HasPrefix(path, xdg) {
			t.Errorf("%s is under XDG_CONFIG_HOME, and golder has one root", path)
		}
	}
	if _, err := os.Stat(xdg); !os.IsNotExist(err) {
		t.Errorf("resolving a workspace created %s", xdg)
	}
}

// TestUnresolvableStateRootIsAnError: golderhome returns "" rather than falling
// back to the working directory, so a workspace says what is missing instead of
// writing a state tree into the person's repository.
func TestUnresolvableStateRootIsAnError(t *testing.T) {
	e := newTestEnv(t)
	e.set(golderhome.EnvHome, "")
	t.Setenv("HOME", "")

	_, err := resolved(t, e)
	if err == nil {
		t.Fatal("resolve succeeded with no state root")
	}
	if !strings.Contains(err.Error(), golderhome.EnvHome) {
		t.Errorf("error %q does not name %s", err, golderhome.EnvHome)
	}
}

// TestSocketIsShortPrivateAndDistinct pins the three properties of the default
// socket path: it lives in the private runtime directory (never bare in /tmp),
// it fits the kernel's limit, and it is distinct per project and per session.
//
// The runtime directory is injected as a string, so this is a test of the
// naming rule rather than of this machine's temp directory.
func TestSocketIsShortPrivateAndDistinct(t *testing.T) {
	getenv := func(k string) string {
		if k == "XDG_RUNTIME_DIR" {
			return "/r"
		}
		return ""
	}
	cases := []struct{ id, session string }{
		{"aaaaaaaaaaaa", "work"},
		{"bbbbbbbbbbbb", "work"},
		{"aaaaaaaaaaaa", "dev"},
		{"aaaaaaaaaaaa", "a b"}, // sanitises to the same "a-b" as the next one
		{"aaaaaaaaaaaa", "a-b"},
	}
	seen := make(map[string]string, len(cases))
	for _, tc := range cases {
		path, dir, err := socketPath(getenv, "/p", tc.id, tc.session, "")
		if err != nil {
			t.Fatalf("socketPath(%q, %q): %v", tc.id, tc.session, err)
		}
		if want := filepath.Join("/r", runtimeDirName); dir != want {
			t.Errorf("dir = %q, want %q", dir, want)
		}
		if !strings.HasPrefix(path, dir+"/") {
			t.Errorf("socket %q is not in %q", path, dir)
		}
		if !strings.HasSuffix(path, ".sock") {
			t.Errorf("socket %q does not end in .sock", path)
		}
		if len(path) > maxSocketPath {
			t.Errorf("socket %q is %d bytes, past the %d-byte limit", path, len(path), maxSocketPath)
		}
		key := tc.id + "/" + tc.session
		if other, ok := seen[path]; ok {
			t.Errorf("%s and %s share the socket %q", key, other, path)
		}
		seen[path] = key
	}
}

// TestSocketFallsBackToAHash: a runtime directory deep enough that the readable
// name no longer fits still gets a socket — a shorter, hash-only name — and a
// path nothing can shorten is refused with the reason.
func TestSocketFallsBackToAHash(t *testing.T) {
	// Long enough that "ws-<session>-<hash>.sock" does not fit, short enough
	// that "ws-<hash>.sock" does.
	deep := "/" + strings.Repeat("a", 59)
	getenv := func(k string) string {
		if k == "XDG_RUNTIME_DIR" {
			return deep
		}
		return ""
	}
	longSession := strings.Repeat("s", 20)
	path, _, err := socketPath(getenv, "/p", "aaaaaaaaaaaa", longSession, "")
	if err != nil {
		t.Fatalf("socketPath: %v", err)
	}
	if len(path) > maxSocketPath {
		t.Errorf("socket %q is %d bytes, past the %d-byte limit", path, len(path), maxSocketPath)
	}
	if strings.Contains(path, longSession) {
		t.Errorf("socket %q kept the readable name, and it does not fit", path)
	}

	// Nothing fits: the message says what to do rather than truncating a path
	// into one nobody can find.
	tooDeep := "/" + strings.Repeat("b", maxSocketPath)
	_, _, err = socketPath(func(k string) string {
		if k == "XDG_RUNTIME_DIR" {
			return tooDeep
		}
		return ""
	}, "/p", "aaaaaaaaaaaa", "work", "")
	if err == nil {
		t.Fatal("socketPath accepted a runtime directory no socket fits in")
	}
	for _, want := range []string{"--socket", "TMPDIR"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// TestCustomSocketIsUsedVerbatim: --socket is the person's decision, so it is
// used as given (made absolute), the private runtime directory is not created
// for it, and a path that cannot be a Unix socket is refused with the numbers.
func TestCustomSocketIsUsedVerbatim(t *testing.T) {
	e := newTestEnv(t)
	custom := filepath.Join(e.base, "x.sock")
	got := mustResolve(t, e, "--socket", custom)
	if got.Socket != custom {
		t.Errorf("socket = %q, want %q", got.Socket, custom)
	}
	if got.SocketDir != "" {
		t.Errorf("socket dir = %q, want none for a custom socket", got.SocketDir)
	}

	// A relative path is read against the project, like every other path.
	if err := os.MkdirAll(filepath.Join(e.project, "sock"), 0o700); err != nil {
		t.Fatal(err)
	}
	rel := mustResolve(t, e, "--socket", "sock/ws.sock")
	if want := filepath.Join(e.project, "sock", "ws.sock"); rel.Socket != want {
		t.Errorf("relative socket = %q, want %q", rel.Socket, want)
	}

	// Too long for sockaddr_un: refused, with the limit and the length, because
	// a name the kernel truncates is a socket nothing can connect to.
	long := filepath.Join(e.base, strings.Repeat("d", maxSocketPath))
	_, err := resolved(t, e, "--socket", long)
	if err == nil {
		t.Fatal("a socket path past the limit was accepted")
	}
	if !strings.Contains(err.Error(), "at most") || !strings.Contains(err.Error(), "--socket") {
		t.Errorf("error %q does not explain the limit", err)
	}

	// A directory that is not there would fail at dial time with a message
	// about something else entirely.
	_, err = resolved(t, e, "--socket", filepath.Join(e.base, "missing", "x.sock"))
	if err == nil || !strings.Contains(err.Error(), "existing directory") {
		t.Errorf("error = %v, want it to name the missing directory", err)
	}
}

// TestStateDirectoryAndConfigOverrides: --state-dir and --config are honoured,
// relative paths are read against the project rather than wherever the command
// happened to be typed, and the log stays under the project's durable
// directory — one project, one log to read.
func TestStateDirectoryAndConfigOverrides(t *testing.T) {
	e := newTestEnv(t)
	absState := filepath.Join(e.base, "abs-state")
	absConfig := filepath.Join(e.base, "abs-config.toml")

	got := mustResolve(t, e, "--state-dir", absState, "--config", absConfig)
	if got.StateDir != absState || got.ConfigPath != absConfig {
		t.Errorf("state/config = %q/%q, want %q/%q", got.StateDir, got.ConfigPath, absState, absConfig)
	}

	rel := mustResolve(t, e, "--state-dir", "st", "--config", "cfg.toml")
	if want := filepath.Join(e.project, "st"); rel.StateDir != want {
		t.Errorf("relative state dir = %q, want %q", rel.StateDir, want)
	}
	if want := filepath.Join(e.project, "cfg.toml"); rel.ConfigPath != want {
		t.Errorf("relative config = %q, want %q", rel.ConfigPath, want)
	}
}

// TestSessionNameRules pins the daemon's session-name rules, because this
// command also uses the name in a socket file name: a slash would escape the
// runtime directory, and a name past the limit is one the daemon refuses.
func TestSessionNameRules(t *testing.T) {
	e := newTestEnv(t)
	cases := []struct{ name, session, want string }{
		{name: "slash", session: "a/b", want: "slash"},
		{name: "backslash", session: "a" + string(rune(92)) + "b", want: "backslash"},
		{name: "control character", session: "a" + string(rune(0)) + "b", want: "control"},
		{name: "too long", session: strings.Repeat("s", maxSessionName+1), want: "at most"},
		{name: "dot", session: ".", want: "not a name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolved(t, e, "--session", tc.session)
			if err == nil {
				t.Fatalf("session %q was accepted", tc.session)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// TestDefaultSessionFitsTheDaemonLimit: a project whose directory name is long
// still gets a session name the daemon accepts, and a readable one.
func TestDefaultSessionFitsTheDaemonLimit(t *testing.T) {
	e := newTestEnv(t)
	long := filepath.Join(e.base, strings.Repeat("n", 120))
	if err := os.MkdirAll(long, 0o700); err != nil {
		t.Fatal(err)
	}
	got := mustResolve(t, e, "--cwd", long)
	if len(got.Session) > maxSessionName {
		t.Errorf("session %q is %d bytes, past the daemon's %d", got.Session, len(got.Session), maxSessionName)
	}
	if !strings.HasPrefix(got.Session, "golder-") {
		t.Errorf("session = %q, want it to say it is golder's", got.Session)
	}
	if err := validateSession(got.Session); err != nil {
		t.Errorf("the derived session name is not one the daemon would accept: %v", err)
	}
}

// TestCwdMustExist: --cwd naming something that is not there is argv this
// command cannot act on, and it says which flag.
func TestCwdMustExist(t *testing.T) {
	e := newTestEnv(t)
	_, err := resolved(t, e, "--cwd", filepath.Join(e.base, "nope"))
	if err == nil || !strings.Contains(err.Error(), "--cwd") {
		t.Fatalf("error = %v, want it to name --cwd", err)
	}

	file := filepath.Join(e.base, "a-file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = resolved(t, e, "--cwd", file)
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("error = %v, want it to say the path is not a directory", err)
	}
}

// TestRuntimeDirFollowsTheEnvironment: the socket directory prefers
// $XDG_RUNTIME_DIR, falls back to $TMPDIR, and ignores a relative value in
// either — a relative runtime directory would put the socket under whatever the
// working directory happens to be.
func TestRuntimeDirFollowsTheEnvironment(t *testing.T) {
	cases := []struct {
		name string
		kv   map[string]string
		want string
	}{
		{name: "XDG_RUNTIME_DIR wins", kv: map[string]string{"XDG_RUNTIME_DIR": "/run/user/1", "TMPDIR": "/tmp"}, want: "/run/user/1/golder"},
		{name: "TMPDIR next", kv: map[string]string{"TMPDIR": "/var/tmp/u"}, want: "/var/tmp/u/golder"},
		{name: "TEMP last", kv: map[string]string{"TEMP": "/tmp/u"}, want: "/tmp/u/golder"},
		{name: "relative ignored", kv: map[string]string{"XDG_RUNTIME_DIR": "run", "TMPDIR": "/tmp/u"}, want: "/tmp/u/golder"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RuntimeDir(func(k string) string { return tc.kv[k] })
			if got != tc.want {
				t.Errorf("RuntimeDir = %q, want %q", got, tc.want)
			}
		})
	}
}
