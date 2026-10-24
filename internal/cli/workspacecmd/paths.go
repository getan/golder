package workspacecmd

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/getan/golder/internal/golderhome"
	"github.com/getan/golder/internal/hostcompat"
	"github.com/getan/golder/internal/privatepath"
)

// Where a workspace keeps its things, and what it is called.
//
// A workspace is about one project and one session inside it. Five paths:
//
//	config  the per-project config.toml the workspace daemon reads
//	state   the daemon's durable state (its session store and whatever follows)
//	log     the daemon's own log
//	socket  the control socket, which is runtime rather than durable
//	root    the project's directory under the state root, which holds all of the
//	        above except the socket
//
// They live under golder's one state root ($GOLDER_HOME, else ~/.golder —
// internal/golderhome decides, and this package never invents a second tree),
// in a directory named after the project's identity. The socket lives somewhere
// short and private instead, because a Unix socket path is limited to about a
// hundred bytes and a project directory is routinely deeper than that; see
// RuntimeDir.
//
// Two of them are scoped by session, not by project. The layout is:
//
//	<Home>/workspace/<project id>/config.toml                        project
//	<Home>/workspace/<project id>/sessions/<session>/state/          session
//	<Home>/workspace/<project id>/sessions/<session>/workspace.log   session
//
// The config describes the checkout and is one file for every session in it.
// State and the log do not survive that treatment: a session store and a log of
// what an agent did are per workspace instance, so two sessions of one project
// asking for the same directory would append two agents' output to one log and
// share one session store. The session name is validated (see validateSession),
// so it is a safe path component, and it is grouped under "sessions/" so a
// session cannot be named after a file this layout already put in the project
// directory ("config.toml", say).
//
// The identity is a hash of the project's canonical path, for the reasons that
// make a name a bad identity: two checkouts of the same repository are both
// called "api", a path can be reached through a symlink that spells it
// differently, and a name is not injective under any sanitisation. Canonical
// path first (absolute, symlinks resolved where they resolve), then sha256, so
// the same project always lands in the same directory and two projects never
// share one.

// Options is one resolved `golder workspace` invocation: everything a backend
// needs, and nothing it has to derive for itself.
type Options struct {
	// Command is the operation being asked for.
	Command Command
	// Paths is where this workspace keeps its files. It is embedded, so
	// opts.StateDir reads as one field and opts.Paths reads as the whole set.
	Paths
	// Cwd is the project directory the workspace's panes start in: absolute,
	// with symlinks resolved where they resolve.
	Cwd string
	// Session is the workspace session name: --session, or one derived from
	// the project. It is also a file name component, so it is validated.
	Session string
	// Socket is the daemon's control socket, absolute.
	Socket string
	// Headless asks for the daemon alone: no client is attached, and Start
	// returns as soon as the workspace is ready.
	Headless bool
	// Shell asks for a workspace whose first pane is a shell rather than an
	// agent. AgentArgs is then empty, by construction (see parsed.validate).
	Shell bool
	// Nested records that --nested was given: this workspace was asked for
	// inside another multiplexer, and the person knows.
	Nested bool
	// Host is the multiplexer this process runs in, as hostcompat names it
	// ("golder-workspace", "tuios", "herdr", "tmux", "zellij", "screen"), or
	// "" in a plain terminal. A backend may use it to say where the workspace
	// was started from; it has already been used for the nesting decision.
	Host string
	// AgentArgs is the argv after "--": the golder agent's own arguments,
	// exactly as typed. It is never joined into a command line.
	AgentArgs []string
	// Appearance is the display settings asked for on the command line.
	Appearance Appearance
}

// Paths are the durable and runtime locations one workspace uses.
type Paths struct {
	// Home is golder's state root: $GOLDER_HOME, else ~/.golder.
	Home string
	// Project is the project's canonical path: absolute, symlinks resolved
	// where they resolve. It is what ProjectID hashes and what Cwd is.
	Project string
	// ProjectID is the stable identity of Project: the first 12 hex
	// characters of its sha256. Two projects never share it.
	ProjectID string
	// ProjectDir is the project's durable directory:
	// <Home>/workspace/<ProjectID>.
	ProjectDir string
	// ConfigPath is the workspace's config file (--config, else config.toml
	// in ProjectDir). It is project-scoped: every session of one checkout reads
	// the same config.
	ConfigPath string
	// SessionDir is this session's durable directory inside ProjectDir,
	// <ProjectDir>/sessions/<session>. It holds the state directory and the log,
	// and is where a backend should put anything else that belongs to one
	// session instance (a pid file, a lock).
	SessionDir string
	// StateDir is the workspace's state directory: --state-dir when it was
	// given, else state/ in SessionDir. The default is per session, so two
	// sessions of one project never share a session store; --state-dir is the
	// person's own decision, and two sessions given the same directory do share
	// it, which is what StateDirExplicit records for a backend that has to
	// arbitrate (its lock decides who owns it, not this package).
	StateDir string
	// StateDirExplicit records that --state-dir named StateDir, so a backend can
	// tell a per-session default from a directory the person deliberately shared
	// between sessions.
	StateDirExplicit bool
	// LogFile is the workspace daemon's log, in SessionDir: one session's
	// history is one file to read, and two sessions never append to one file.
	LogFile string
	// SocketDir is the private directory the default socket is created in, and
	// "" when --socket named a path (a custom socket is used verbatim, and its
	// directory is the person's to manage).
	SocketDir string
}

// Names this package writes under the state root. They are constants so the
// audit question "what does a workspace write" is answered in one place.
const (
	// workspaceDirName groups every project's workspace files under the state
	// root: <Home>/workspace/<project id>/.
	workspaceDirName = "workspace"
	// sessionsDirName groups one project's session directories, so a session
	// name can never collide with the project's own files: <ProjectDir>/sessions/.
	sessionsDirName = "sessions"
	// stateDirName is the per-session state directory inside SessionDir.
	stateDirName = "state"
	// logFileName is the daemon's log inside SessionDir.
	logFileName = "workspace.log"
	// maxSocketPath is the longest socket path this command will hand a
	// daemon. A Unix socket path must fit sockaddr_un.sun_path (104 bytes on
	// macOS, 108 on Linux, including the terminator), so 100 is a limit that
	// works on both with room to spare.
	maxSocketPath = 100
	// maxSessionName is the daemon's own limit on a session name, in bytes.
	maxSessionName = 64
)

// resolve reads the machine and turns one parsed argv into the Options a
// backend acts on. It is the only place the environment, the working directory
// and the filesystem are read, which is what makes the rest of the package
// testable without any of them.
func resolve(p parsed, deps Deps) (Options, error) {
	opts := Options{
		Command:    p.command,
		Headless:   p.headless,
		Shell:      p.shell,
		Nested:     p.nested,
		AgentArgs:  p.agentArgs,
		Appearance: p.appear,
	}

	cwd, err := projectPath(p.cwd, deps)
	if err != nil {
		return Options{}, err
	}
	opts.Cwd = cwd

	home := strings.TrimSpace(golderhome.DirFrom(deps.Getenv))
	if home == "" {
		// golderhome returns "" rather than falling back to the working
		// directory, where a state file would be a surprise in the person's
		// repository. A workspace cannot run without a state root, so it says
		// what is missing instead of writing somewhere else.
		return Options{}, fmt.Errorf("cannot resolve golder's state root (%s is unset and the home directory is unknown); set %s",
			golderhome.EnvHome, golderhome.EnvHome)
	}

	id := projectID(cwd)
	projectDir := filepath.Join(home, workspaceDirName, id)
	// The session is resolved before the paths because two of them are scoped by
	// it: state and the log belong to one workspace instance, not to the project.
	opts.Session = p.session
	if opts.Session == "" {
		opts.Session = defaultSession(cwd, id)
	}
	if err := validateSession(opts.Session); err != nil {
		return Options{}, err
	}
	sessionDir := filepath.Join(projectDir, sessionsDirName, opts.Session)
	opts.Paths = Paths{
		Home:       home,
		Project:    cwd,
		ProjectID:  id,
		ProjectDir: projectDir,
		// The config describes the checkout, so every session in it reads the
		// same file; --config still names one file for the whole project.
		ConfigPath:       joinIfSet(cwd, p.config, filepath.Join(projectDir, golderhome.ConfigFile)),
		SessionDir:       sessionDir,
		StateDir:         joinIfSet(cwd, p.stateDir, filepath.Join(sessionDir, stateDirName)),
		StateDirExplicit: p.stateDir != "",
		LogFile:          filepath.Join(sessionDir, logFileName),
	}

	opts.Host = hostcompat.Resolve(deps.Getenv).Multiplexer
	// Starting a workspace inside another multiplexer nests one terminal in
	// another, which is almost never what someone typing the command meant;
	// --nested says they did. status and stop are exempt: they talk to a
	// workspace over a socket and are perfectly sensible from inside one.
	if opts.Command == CommandRun && !opts.Nested {
		if err := refuseNesting(opts.Host); err != nil {
			return Options{}, err
		}
	}

	opts.Socket, opts.SocketDir, err = socketPath(deps.Getenv, cwd, id, opts.Session, p.socket)
	if err != nil {
		return Options{}, err
	}

	return opts, nil
}

// projectPath resolves the project directory: --cwd when it was given, else the
// working directory, canonicalised, and checked to exist. A typo in --cwd is
// argv this command cannot act on (exit 2); a working directory that cannot be
// read is a failure (exit 1).
func projectPath(flag string, deps Deps) (string, error) {
	raw := flag
	if raw == "" {
		got, err := deps.Getwd()
		if err != nil {
			return "", fmt.Errorf("cannot read the working directory: %w", err)
		}
		raw = got
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", usagef("--cwd %q cannot be made absolute: %v", raw, err)
	}
	// Symlinks are resolved where they resolve. A path that does not resolve
	// (a broken link, a platform without them) is left as it is rather than
	// refused: the identity only has to be stable, and the same spelling is
	// stable too.
	if resolved, rerr := filepath.EvalSymlinks(abs); rerr == nil {
		abs = resolved
	}
	info, err := os.Stat(abs)
	if err != nil {
		if flag != "" {
			return "", usagef("--cwd %q is not a directory this command can open: %v", flag, err)
		}
		return "", fmt.Errorf("cannot open the working directory %q: %w", abs, err)
	}
	if !info.IsDir() {
		return "", usagef("--cwd %q is not a directory", flag)
	}
	return abs, nil
}

// projectID is the stable identity of a canonical project path: the first 12
// hex characters of sha256 of the path. It is not a security boundary, it is an
// equality test — 48 bits is far more than the number of projects one person
// opens, and a collision would only make two of them share a directory.
func projectID(canonical string) string {
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])[:12]
}

// joinIfSet returns abs (the flag's value, resolved against the project when it
// is relative) or fallback when the flag was not given. A relative --config or
// --state-dir is read against the project directory, not the process's working
// directory, so the two agree no matter where the command was typed from.
func joinIfSet(cwd, abs, fallback string) string {
	if abs == "" {
		return fallback
	}
	if filepath.IsAbs(abs) {
		return filepath.Clean(abs)
	}
	return filepath.Join(cwd, abs)
}

// defaultSession names a workspace nobody named: golder-<project>-<id>, where
// the id is short for the project's identity. It is stable across runs and
// distinct between two checkouts that are both called "api", which is the whole
// point — a session name is what the daemon's own session store keys on, so a
// name shared by two projects would share their history.
func defaultSession(project, id string) string {
	base := sanitizeName(filepath.Base(project))
	// Keep the whole name inside the daemon's limit with room for the prefix,
	// the id and a dash either side.
	const maxBase = maxSessionName - len("golder-") - len("-123456") - 4
	if len(base) > maxBase {
		base = strings.Trim(base[:maxBase], "-._")
	}
	if base == "" {
		base = "project"
	}
	return "golder-" + base + "-" + id[:6]
}

// validateSession applies the daemon's session-name rules, because this package
// also uses the name as a file name component: no slash, no backslash, no
// control character, at most 64 bytes. "." and ".." are refused for the same
// reason a path component is.
func validateSession(name string) error {
	switch {
	case name == "":
		return usagef("--session needs a name")
	case len(name) > maxSessionName:
		return usagef("--session %q is %d bytes; the workspace daemon allows at most %d", name, len(name), maxSessionName)
	case name == "." || name == "..":
		return usagef("--session %q is not a name", name)
	}
	for _, r := range name {
		switch {
		case r == '/':
			return usagef("--session %q contains a slash, which the workspace daemon does not allow", name)
		case r == '\\':
			return usagef("--session %q contains a backslash, which the workspace daemon does not allow", name)
		case unicode.IsControl(r):
			return usagef("--session %q contains a control character", name)
		}
	}
	return nil
}

// sanitizeName folds a string into a name safe for a session, a socket file
// name and a shell prompt: letters, digits, dot, dash and underscore, with any
// other run folded into a single dash. It is the same rule the workspace daemon
// uses to name a session after a directory.
func sanitizeName(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
			b.WriteRune(r)
			dash = false
		default:
			if !dash {
				b.WriteRune('-')
				dash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// RuntimeDir is the short, private directory default sockets live in:
// $XDG_RUNTIME_DIR/golder, else $TMPDIR/golder-<uid>, else /tmp/golder-<uid>. It
// is exported because a backend writing a pid file or a lock beside the socket
// should use the same directory rather than guess at one.
//
// A socket path is limited by the kernel to about a hundred bytes, so this is
// deliberately not under the state root: $GOLDER_HOME can be arbitrarily deep,
// and a project directory is usually deeper still. Neither is it in the runtime
// directory bare: a socket in a world-writable /tmp is a socket anyone can
// connect to, so it sits one level down, in a directory this command creates
// 0700 and keeps that way (see ensurePrivateDir). Under a shared temp directory
// the name carries the uid, so two accounts on one machine cannot contend for
// the same socket directory, pid file or lock; see privatepath.RuntimeRoot,
// which owns the rule.
func RuntimeDir(getenv func(string) string) string { return privatepath.RuntimeRoot(getenv) }

// socketPath returns the daemon socket for one workspace, and the private
// directory it will be created in ("" when --socket named the path).
func socketPath(getenv func(string) string, cwd, id, session, custom string) (path, dir string, err error) {
	if custom != "" {
		if !filepath.IsAbs(custom) {
			custom = filepath.Join(cwd, custom)
		}
		custom = filepath.Clean(custom)
		if len(custom) > maxSocketPath {
			return "", "", usagef("--socket %q is %d bytes; a Unix socket path must be at most %d (try a shorter directory)",
				custom, len(custom), maxSocketPath)
		}
		info, serr := os.Stat(filepath.Dir(custom))
		if serr != nil {
			return "", "", usagef("--socket %q needs an existing directory: %v", custom, serr)
		}
		if !info.IsDir() {
			return "", "", usagef("--socket %q needs a directory, and %q is not one", custom, filepath.Dir(custom))
		}
		return custom, "", nil
	}

	dir = RuntimeDir(getenv)
	// A name a person can read, with a hash so two sessions cannot collide
	// after sanitising; then a hash-only fallback for a runtime directory deep
	// enough that the readable name no longer fits.
	sum := sha256.Sum256([]byte(id + "\x00" + session))
	digest := hex.EncodeToString(sum[:])
	short := sanitizeName(session)
	if len(short) > 24 {
		short = strings.Trim(short[:24], "-._")
	}
	var names []string
	if short != "" {
		names = append(names, "ws-"+short+"-"+digest[:6]+".sock")
	}
	names = append(names, "ws-"+digest[:10]+".sock")
	for _, name := range names {
		candidate := filepath.Join(dir, name)
		if len(candidate) <= maxSocketPath {
			return candidate, dir, nil
		}
	}
	return "", "", fmt.Errorf("cannot place the workspace socket under %q: every name exceeds the %d-byte limit a Unix socket path allows; pass --socket with a shorter path or set %s",
		dir, maxSocketPath, "TMPDIR")
}

// ensureDirs creates the directories this workspace writes into, 0700, and
// keeps them that way. It runs for a start and not for status or stop: a
// command that only reads or only stops must not leave a directory tree behind.
func (o Options) ensureDirs() error {
	for _, dir := range []string{o.ProjectDir, o.SessionDir, o.StateDir, o.SocketDir} {
		if dir == "" {
			continue
		}
		if err := ensurePrivateDir(dir); err != nil {
			return err
		}
	}
	return nil
}

// ensurePrivateDir creates dir with mode 0700 and refuses to leave it readable
// by anyone else: these directories hold a session store, a trust table and a
// log of what an agent did, and a directory that is 0755 because of an old
// umask leaks all three to every other user on the machine.
//
// A directory that already exists with wider permissions is tightened rather
// than refused — it is golder's own subdirectory, so 0700 is strictly what it
// should always have been — and a directory that cannot be tightened is an
// error that names the mode, because silently writing private state into a
// shared directory is the one outcome worth stopping for.
//
// The work is privatepath's, so the same rules apply here and to the browser
// tool's directories: nothing is created or tightened through a symlink, and a
// directory another account owns is refused rather than adopted. That last rule
// is what makes the uid-scoped runtime name more than a naming convention: if
// someone else already created this account's runtime directory, the start
// fails loudly instead of putting a control socket in it.
func ensurePrivateDir(dir string) error { return privatepath.EnsureDir(dir) }

// reporting applies what a backend reported back onto the options, so a
// headless readiness document names the session and socket that actually exist
// rather than the ones that were asked for.
func (o Options) reporting(res StartResult) Options {
	if s := strings.TrimSpace(res.Session); s != "" {
		o.Session = s
	}
	if s := strings.TrimSpace(res.Socket); s != "" {
		o.Socket = s
	}
	return o
}

// refuseNesting says why a workspace will not be started inside another
// multiplexer, and names the flag that says "yes, I meant it". It returns nil
// in a plain terminal. The multiplexer is named the way each one is named to a
// person: golder's own workspace is not "golder-workspace" in a sentence.
func refuseNesting(host string) error {
	switch host {
	case hostcompat.MuxNone:
		return nil
	case hostcompat.MuxGolder:
		return fmt.Errorf("this is a pane of golder's own workspace, and a workspace started here would nest inside it; pass --nested to do that anyway")
	default:
		return fmt.Errorf("this is a %s pane, and a workspace started here would nest one multiplexer inside another; pass --nested to do that anyway", host)
	}
}
