package workspacecmd

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/getan/golder/internal/golderhome"
	"github.com/getan/golder/internal/privatepath"
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
		if want := RuntimeDir(getenv); dir != want {
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
//
// The fallback name carries the uid, which is the fix for a shared machine:
// /tmp/golder used to be one directory for every account on it. $XDG_RUNTIME_DIR
// is per-user by contract, so it needs no suffix.
func TestRuntimeDirFollowsTheEnvironment(t *testing.T) {
	scoped := privatepath.RuntimeName()
	if !strings.HasPrefix(scoped, "golder") {
		t.Fatalf("RuntimeName() = %q, want a name that says it is golder's", scoped)
	}
	if runtime.GOOS != "windows" && runtime.GOOS != "plan9" && runtime.GOOS != "js" {
		if want := "golder-" + strconv.Itoa(os.Getuid()); scoped != want {
			t.Errorf("RuntimeName() = %q, want %q: a shared temp directory must be scoped by uid", scoped, want)
		}
	}
	cases := []struct {
		name string
		kv   map[string]string
		want string
	}{
		{name: "XDG_RUNTIME_DIR wins", kv: map[string]string{"XDG_RUNTIME_DIR": "/run/user/1", "TMPDIR": "/tmp"}, want: "/run/user/1/golder"},
		{name: "TMPDIR next", kv: map[string]string{"TMPDIR": "/var/tmp/u"}, want: filepath.Join("/var/tmp/u", scoped)},
		{name: "TEMP last", kv: map[string]string{"TEMP": "/tmp/u"}, want: filepath.Join("/tmp/u", scoped)},
		{name: "relative ignored", kv: map[string]string{"XDG_RUNTIME_DIR": "run", "TMPDIR": "/tmp/u"}, want: filepath.Join("/tmp/u", scoped)},
		{name: "relative XDG alone ignored", kv: map[string]string{"XDG_RUNTIME_DIR": "run"}, want: filepath.Join(os.TempDir(), scoped)},
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

// TestSessionStateLayout pins where one session's durable files go. The shape is
// the contract a backend reads: config is per project, state and the log are per
// session, and the session directory is the place for anything else that belongs
// to one workspace instance.
func TestSessionStateLayout(t *testing.T) {
	e := newTestEnv(t)
	got := mustResolve(t, e, "--session", "work")

	projectDir := filepath.Join(e.home, workspaceDirName, got.ProjectID)
	sessionDir := filepath.Join(projectDir, sessionsDirName, "work")
	for name, pair := range map[string][2]string{
		"project dir": {got.ProjectDir, projectDir},
		"session dir": {got.SessionDir, sessionDir},
		"state dir":   {got.StateDir, filepath.Join(sessionDir, stateDirName)},
		"log":         {got.LogFile, filepath.Join(sessionDir, logFileName)},
		"config":      {got.ConfigPath, filepath.Join(projectDir, golderhome.ConfigFile)},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s = %q, want %q", name, pair[0], pair[1])
		}
	}
	for _, path := range []string{got.ProjectDir, got.SessionDir, got.StateDir, got.LogFile} {
		if !strings.HasPrefix(path, e.home) {
			t.Errorf("%q is outside the one state root %q", path, e.home)
		}
	}
}

// TestSessionsOfOneProjectKeepSeparateStateAndLog is the collision the session
// directory exists to prevent. Two workspaces on one checkout are two daemons
// with two session stores, and they used to write into one state directory and
// append to one log; a person running both lost half the history of each. The
// project itself — its identity and its config — is still shared, because that
// is what "the same project" means.
func TestSessionsOfOneProjectKeepSeparateStateAndLog(t *testing.T) {
	e := newTestEnv(t)
	work := mustResolve(t, e, "--session", "work")
	personal := mustResolve(t, e, "--session", "personal")

	for name, pair := range map[string][2]string{
		"project id":  {work.ProjectID, personal.ProjectID},
		"project dir": {work.ProjectDir, personal.ProjectDir},
		"config":      {work.ConfigPath, personal.ConfigPath},
	} {
		if pair[0] != pair[1] {
			t.Errorf("two sessions of one project disagree about its %s: %q and %q", name, pair[0], pair[1])
		}
	}
	for name, pair := range map[string][2]string{
		"session dir": {work.SessionDir, personal.SessionDir},
		"state dir":   {work.StateDir, personal.StateDir},
		"log":         {work.LogFile, personal.LogFile},
		"socket":      {work.Socket, personal.Socket},
	} {
		if pair[0] == pair[1] {
			t.Errorf("two sessions of one project share a %s: %q", name, pair[0])
		}
	}
	for _, opts := range []Options{work, personal} {
		if filepath.Dir(opts.StateDir) != opts.SessionDir {
			t.Errorf("state dir %q is not in its session directory %q", opts.StateDir, opts.SessionDir)
		}
		if filepath.Dir(opts.LogFile) != opts.SessionDir {
			t.Errorf("log %q is not in its session directory %q", opts.LogFile, opts.SessionDir)
		}
		if !strings.HasPrefix(opts.SessionDir, opts.ProjectDir+string(filepath.Separator)) {
			t.Errorf("session directory %q is outside the project directory %q", opts.SessionDir, opts.ProjectDir)
		}
	}
}

// TestOneSessionsLogIsNotTouchedByAnother is the same collision at the level a
// person notices: a second start must not write into the first session's log,
// and must not reuse its state directory.
func TestOneSessionsLogIsNotTouchedByAnother(t *testing.T) {
	e := newTestEnv(t)
	first := &recorder{}
	if _, stderr, code := e.run(t, first, "--session", "work"); code != ExitOK {
		t.Fatalf("first start exit = %d, want 0 (stderr: %s)", code, stderr)
	}
	work := first.started[0]
	marker := []byte("what the work session's agent did\n")
	if err := os.WriteFile(work.LogFile, marker, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work.StateDir, "sessions.json"), []byte("work"), 0o600); err != nil {
		t.Fatal(err)
	}

	second := &recorder{}
	if _, stderr, code := e.run(t, second, "--session", "personal"); code != ExitOK {
		t.Fatalf("second start exit = %d, want 0 (stderr: %s)", code, stderr)
	}
	personal := second.started[0]
	if personal.LogFile == work.LogFile || personal.StateDir == work.StateDir {
		t.Fatalf("the second session reused %q / %q", personal.LogFile, personal.StateDir)
	}

	got, err := os.ReadFile(work.LogFile)
	if err != nil {
		t.Fatalf("the first session's log is gone: %v", err)
	}
	if !bytes.Equal(got, marker) {
		t.Errorf("the first session's log became %q, want %q", got, marker)
	}
	if _, err := os.Stat(filepath.Join(work.StateDir, "sessions.json")); err != nil {
		t.Errorf("the first session's state was lost: %v", err)
	}
	for _, dir := range []string{personal.SessionDir, personal.StateDir} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("%s was not created: %v", dir, err)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Errorf("%s is mode %04o, want 0700", dir, perm)
		}
	}
}

// TestExplicitStateDirIsRecordedAndNamed: --state-dir is the person's decision,
// so it is used as given for every session, and StateDirExplicit says so. Two
// sessions given one directory therefore share it — the workspace daemon's own
// lock decides who owns it, not this command — while their logs stay separate,
// because a log is per session no matter where the state lives.
func TestExplicitStateDirIsRecordedAndNamed(t *testing.T) {
	e := newTestEnv(t)
	if got := mustResolve(t, e); got.StateDirExplicit {
		t.Errorf("the default state dir was reported as explicit: %q", got.StateDir)
	}

	work := mustResolve(t, e, "--session", "work", "--state-dir", "shared-state")
	want := filepath.Join(e.project, "shared-state")
	if work.StateDir != want {
		t.Errorf("state dir = %q, want %q", work.StateDir, want)
	}
	if !work.StateDirExplicit {
		t.Error("--state-dir was not recorded on the options")
	}
	personal := mustResolve(t, e, "--session", "personal", "--state-dir", want)
	if personal.StateDir != work.StateDir {
		t.Errorf("an explicitly shared state directory became %q and %q", work.StateDir, personal.StateDir)
	}
	if personal.LogFile == work.LogFile {
		t.Errorf("two sessions sharing a state directory also share the log %q", work.LogFile)
	}
}

// TestStartRefusesASymlinkedDirectory: a directory this command writes into is
// reached without following a link. A checkout, or another account, can create
// the state directory as a symlink to somewhere else; chmod through it would
// change the mode of a directory golder does not own, and writing a session
// store through it would put the person's history there.
func TestStartRefusesASymlinkedDirectory(t *testing.T) {
	e := newTestEnv(t)
	opts := mustResolve(t, e)
	target := filepath.Join(e.base, "elsewhere")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(opts.SessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, opts.StateDir); err != nil {
		t.Skipf("this platform has no symlinks: %v", err)
	}

	rec := &recorder{}
	stdout, stderr, code := e.run(t, rec)
	if code != ExitFailure {
		t.Fatalf("exit = %d, want 1 (stdout: %q stderr: %q)", code, stdout, stderr)
	}
	if rec.calls() != 0 {
		t.Error("a start wrote into a symlinked state directory")
	}
	if !strings.Contains(stderr, "symbolic link") {
		t.Errorf("stderr = %q, want it to say the directory is a link", stderr)
	}
	info, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o755 {
		t.Errorf("the link's target is now mode %04o, want it untouched at 0755", perm)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("the link's target gained %d entries, want none", len(entries))
	}
}

// TestStartRefusesADirectoryAnotherUserOwns: on a machine with several accounts,
// a directory another account created under this account's name is refused
// rather than adopted — the runtime directory is uid-scoped so this should not
// happen, and the check is what makes the scoping meaningful.
func TestStartRefusesADirectoryAnotherUserOwns(t *testing.T) {
	e := newTestEnv(t)
	opts := mustResolve(t, e)
	if err := os.MkdirAll(opts.StateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	other := os.Geteuid() + 1000
	if err := os.Chown(opts.StateDir, other, other); err != nil {
		t.Skipf("cannot hand a directory to another user here (%v); this check needs a multi-user filesystem", err)
	}

	rec := &recorder{}
	_, stderr, code := e.run(t, rec)
	if code != ExitFailure {
		t.Fatalf("exit = %d, want 1 (stderr: %s)", code, stderr)
	}
	if rec.calls() != 0 {
		t.Error("a start used another account's directory")
	}
	if !strings.Contains(stderr, "another user") && !strings.Contains(stderr, "another account") {
		t.Errorf("stderr = %q, want it to say the directory is someone else's", stderr)
	}
	info, err := os.Lstat(opts.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o755 {
		t.Errorf("the other account's directory is now mode %04o, want it untouched at 0755", perm)
	}
}

// TestSocketFitsInAUidScopedRuntimeDir: naming the fallback runtime directory
// after the uid costs a few bytes, and a socket path has a hard limit. A long
// session name in a moderately deep temp directory must still produce a socket
// the kernel can bind.
func TestSocketFitsInAUidScopedRuntimeDir(t *testing.T) {
	deep := "/" + strings.Repeat("t", 40)
	getenv := func(k string) string {
		if k == "TMPDIR" {
			return deep
		}
		return ""
	}
	path, dir, err := socketPath(getenv, "/p", "aaaaaaaaaaaa", strings.Repeat("s", maxSessionName), "")
	if err != nil {
		t.Fatalf("socketPath: %v", err)
	}
	if !strings.HasPrefix(dir, filepath.Join(deep, privatepath.RuntimeName())) {
		t.Errorf("dir = %q, want it under the per-user runtime root in %q", dir, deep)
	}
	if len(path) > maxSocketPath {
		t.Errorf("socket %q is %d bytes, past the %d-byte limit", path, len(path), maxSocketPath)
	}
}
