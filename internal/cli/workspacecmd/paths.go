package workspacecmd

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/getan/golder/internal/golderhome"
	"github.com/getan/golder/internal/hostcompat"
	"github.com/getan/golder/internal/privatepath"
)

// Names are global within golder's one canonical state root. The layout is:
//
//	<Home>/workspace/sessions/<name>/session.json
//	<Home>/workspace/sessions/<name>/config.toml
//	<Home>/workspace/sessions/<name>/state/
//	<Home>/workspace/sessions/<name>/workspace.log
//
// Cwd is a saved startup property, not an identity. Socket names hash Home and
// the session name, but live under a short private runtime directory to fit
// Unix-domain limits. The pre-global project layout is read-only through
// --legacy; it is never silently moved or adopted as a named session.

// Options is one resolved `golder workspace` invocation: everything a backend
// needs, and nothing it has to derive for itself.
type Options struct {
	// Command is the operation being asked for.
	Command Command
	// Paths is where this workspace keeps its files. It is embedded, so
	// opts.StateDir reads as one field and opts.Paths reads as the whole set.
	Paths
	// Cwd is the session's saved initial pane directory, or an explicit update
	// for a stopped session. It need not exist to query or attach a live owner.
	Cwd string
	// Session is the name (-s/--session, default "default").
	Session string
	// ManifestPath is where this session's record lives. Empty for a legacy
	// workspace, which predates the record.
	ManifestPath string
	// Exists reports that a session record (or a legacy session directory) is
	// already on disk. It is what distinguishes "run this stopped session" from
	// "create it".
	Exists bool
	// Legacy marks a session in the pre-global layout. It can be attached,
	// inspected and stopped, but never created or removed through the new
	// commands.
	Legacy bool
	// CwdExplicit records that -C/--cwd was given. On an existing session it
	// is an update to the saved start directory, not a lookup key.
	CwdExplicit bool
	// SocketExplicit records that --socket was given for a session that is
	// being created. An existing session keeps its recorded endpoint.
	SocketExplicit bool
	// ConfigExplicit records that --config was given for a new session.
	ConfigExplicit bool
	// StateDirExplicit records that --state-dir named StateDir.
	StateDirExplicit bool
	// CreatedAt is the first time this session was created, carried through
	// saves so a rewritten record does not claim to be new.
	CreatedAt string
	// JSON asks list for its machine-readable document.
	JSON bool
	// DiscoveryError keeps a broken record visible in list without probing an
	// endpoint taken from untrusted/incomplete metadata.
	DiscoveryError string
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
	// Project is the legacy reporting alias of Cwd, not an ownership boundary.
	Project string
	// ProjectID is populated only for a pre-global legacy session.
	ProjectID string
	// ProjectDir is the old project directory for legacy sessions and the
	// identity directory (SessionDir) for new named sessions.
	ProjectDir string
	// ConfigPath is per named session; an explicit override is recorded.
	ConfigPath string
	// SessionDir holds this session's record, log, owner proofs and default state.
	SessionDir string
	// StateDir is the workspace's state directory: --state-dir when it was
	// given, else state/ in SessionDir. The default is per session, so two
	// sessions of one project never share a session store; --state-dir is the
	// person's own decision, and two sessions given the same directory do share
	// it, which is what StateDirExplicit records for a backend that has to
	// arbitrate (its lock decides who owns it, not this package).
	StateDir string
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
	// workspaceDirName groups all workspace state under golder's home.
	workspaceDirName = "workspace"
	// defaultSessionName is the session `golder workspace` addresses when no
	// name is given. Its directory is not special: every named session has the
	// same shape.
	defaultSessionName = "default"
	// sessionsDirName is used by both the old and new layout.
	sessionsDirName       = "sessions"
	globalSessionsDirName = sessionsDirName
	// manifestFileName is one session's identity record inside SessionDir.
	manifestFileName = "session.json"
	// stateDirName is the per-session state directory inside SessionDir.
	stateDirName = "state"
	// logFileName is the daemon's log inside SessionDir.
	logFileName = "workspace.log"
	// maxSocketPath is the longest socket path this command will hand a
	// daemon. A Unix socket path must fit sockaddr_un.sun_path (104 bytes on
	// macOS, including the terminator). The retained daemon also opens the
	// .link-human companion, so reserve its 11 bytes rather than validating
	// only the primary socket and failing after startup has begun.
	maxSocketPath = 92
	// maxSessionName is the daemon's own limit on a session name, in bytes.
	maxSessionName = 64
)

// resolve reads the machine and turns one parsed argv into the Options a
// backend acts on. It is the only place the environment, the working directory
// and the filesystem are read, which is what makes the rest of the package
// testable without any of them.
//
// The identity of a named session is its name under the one golder state root,
// not the directory the command was typed in. A session record is therefore
// readable before the working directory is consulted, and attach/status/stop do
// not require the saved start directory to still exist.
func resolve(p parsed, deps Deps) (Options, error) {
	opts := Options{
		Command:          p.command,
		Headless:         p.headless,
		Shell:            p.shell,
		Nested:           p.nested,
		AgentArgs:        p.agentArgs,
		Appearance:       p.appear,
		JSON:             p.json,
		CwdExplicit:      p.cwd != "",
		SocketExplicit:   p.socket != "",
		ConfigExplicit:   p.config != "",
		StateDirExplicit: p.stateDir != "",
	}
	home, err := stateRoot(deps)
	if err != nil {
		return Options{}, err
	}
	opts.Home = home
	opts.Host = hostcompat.Resolve(deps.Getenv).Multiplexer
	if p.command == CommandList {
		return opts, nil
	}

	if p.legacy != "" {
		return resolveLegacy(p, opts)
	}

	opts.Session = p.session
	if opts.Session == "" {
		opts.Session = defaultSessionName
	}
	if err := validateName(opts.Session); err != nil {
		return Options{}, err
	}
	sessionDir := filepath.Join(home, workspaceDirName, globalSessionsDirName, opts.Session)
	opts.Paths = Paths{
		Home:       home,
		ProjectDir: sessionDir,
		ConfigPath: filepath.Join(sessionDir, golderhome.ConfigFile),
		SessionDir: sessionDir,
		StateDir:   filepath.Join(sessionDir, stateDirName),
		LogFile:    filepath.Join(sessionDir, logFileName),
	}
	opts.ManifestPath = filepath.Join(sessionDir, manifestFileName)

	// The record is read first. It carries the endpoint and the saved start
	// directory, neither of which may depend on where this command runs.
	record, exists, err := loadRecord(opts)
	if err != nil {
		return Options{}, err
	}
	if exists {
		opts.Exists = true
		opts.CreatedAt = record.CreatedAt
		if !opts.SocketExplicit && record.Socket != "" {
			opts.Socket = record.Socket
			opts.SocketDir = record.SocketDir
		}
		if !opts.ConfigExplicit && record.Config != "" {
			opts.ConfigPath = record.Config
		}
		if !opts.StateDirExplicit && record.StateDir != "" {
			opts.StateDir = record.StateDir
		}
		if record.Log != "" {
			opts.LogFile = record.Log
		}
		if !opts.CwdExplicit {
			opts.Cwd = record.Cwd
		}
	}

	// Only creating a session needs the working directory. Lookups must work
	// from anywhere, including after the start directory has been moved.
	if opts.CwdExplicit {
		cwd, err := projectPath(p.cwd, deps)
		if err != nil {
			return Options{}, err
		}
		opts.Cwd = cwd
	} else if !opts.Exists && (opts.Command == CommandRun || opts.Command == CommandNew) {
		cwd, err := projectPath("", deps)
		if err != nil {
			return Options{}, err
		}
		opts.Cwd = cwd
	}
	opts.Project = opts.Cwd

	// Explicit path flags are only accepted on run/new (see parse), where the
	// cwd is known. On an existing session they may update the record only if
	// they agree with it; the backend decides that under its session lock.
	if opts.Cwd != "" {
		if p.config != "" {
			opts.ConfigPath = joinIfSet(opts.Cwd, p.config, opts.ConfigPath)
		}
		if p.stateDir != "" {
			opts.StateDir = joinIfSet(opts.Cwd, p.stateDir, opts.StateDir)
		}
		if p.socket != "" {
			opts.Socket, opts.SocketDir, err = namedSocketPath(deps.Getenv, home, opts.Session, p.socket, opts.Cwd)
			if err != nil {
				return Options{}, err
			}
		}
	}
	if opts.Socket == "" && (opts.Command == CommandRun || opts.Command == CommandNew) {
		opts.Socket, opts.SocketDir, err = namedSocketPath(deps.Getenv, home, opts.Session, "", "")
		if err != nil {
			return Options{}, err
		}
	}

	// A recorded session keeps its endpoint, config and state. Repeating a
	// different value would silently address a different daemon.
	if exists {
		for flag, pair := range map[string][2]string{
			"--socket":    {opts.Socket, record.Socket},
			"--config":    {opts.ConfigPath, record.Config},
			"--state-dir": {opts.StateDir, record.StateDir},
		} {
			if pair[1] != "" && pair[0] != pair[1] {
				return Options{}, usagef("session %q is recorded with %s %q; %s cannot retarget it", opts.Session, flag, pair[1], pair[0])
			}
		}
	}

	// Starting a workspace inside another multiplexer nests one terminal in
	// another, which is almost never what someone typing the command meant;
	// --nested says they did. Everything else talks to a session over a socket
	// and is perfectly sensible from inside one.
	if (opts.Command == CommandRun || opts.Command == CommandNew || opts.Command == CommandAttach) && !opts.Nested {
		if err := refuseNesting(opts.Host); err != nil {
			return Options{}, err
		}
	}

	return opts, nil
}

// stateRoot resolves golder's one durable root. It is read without the working
// directory on purpose: a session must not move because a command was typed
// elsewhere.
func stateRoot(deps Deps) (string, error) {
	home := strings.TrimSpace(golderhome.DirFrom(deps.Getenv))
	if home == "" {
		// golderhome returns "" rather than falling back to the working
		// directory, where a state file would be a surprise in the person's
		// repository. A workspace cannot run without a state root, so it says
		// what is missing instead of writing somewhere else.
		return "", fmt.Errorf("cannot resolve golder's state root (%s is unset and the home directory is unknown); set %s",
			golderhome.EnvHome, golderhome.EnvHome)
	}
	abs, err := filepath.Abs(home)
	if err != nil {
		return "", fmt.Errorf("cannot resolve golder's state root: %w", err)
	}
	// Canonicalise the existing prefix even on first use, when the final state
	// directory does not exist yet. A home reached through /var vs /private/var
	// must not become two socket identities after the directory is created.
	return canonicalRoot(abs), nil
}

func canonicalRoot(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	parent := filepath.Dir(path)
	if parent == path {
		return path
	}
	return filepath.Join(canonicalRoot(parent), filepath.Base(path))
}

// resolveLegacy addresses a session in the pre-global project layout. It is
// deliberately lookup-only: the old records do not carry enough identity to
// create a replacement safely, and a name must never select an arbitrary old
// project.
func resolveLegacy(p parsed, opts Options) (Options, error) {
	id, session, err := legacySpec(p.legacy)
	if err != nil {
		return Options{}, err
	}
	projectDir := filepath.Join(opts.Home, workspaceDirName, id)
	sessionDir := filepath.Join(projectDir, sessionsDirName, session)
	opts.Session = session
	opts.Legacy = true
	opts.Paths = Paths{
		Home:       opts.Home,
		ProjectID:  id,
		ProjectDir: projectDir,
		ConfigPath: filepath.Join(projectDir, golderhome.ConfigFile),
		SessionDir: sessionDir,
		StateDir:   filepath.Join(sessionDir, stateDirName),
		LogFile:    filepath.Join(sessionDir, logFileName),
	}
	if err := checkDirectories(opts.Home, sessionDir); err != nil {
		return Options{}, err
	}
	if _, statErr := os.Stat(sessionDir); statErr == nil {
		opts.Exists = true
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return Options{}, fmt.Errorf("legacy workspace %q: %w", p.legacy, statErr)
	}
	record, found, err := findLegacyOwner(sessionDir, session)
	if err != nil {
		return Options{}, err
	}
	if found {
		opts.Exists = true
		opts.Cwd = record.Project
		opts.Socket = record.Socket
		opts.ConfigPath = record.Config
		opts.StateDir = record.StateDir
		opts.LogFile = record.Log
	}
	opts.Project = opts.Cwd
	if opts.Socket == "" {
		opts.DiscoveryError = "legacy session has no ownership record; its old endpoint is unknown"
	}
	if opts.Command == CommandAttach && !opts.Nested {
		if err := refuseNesting(opts.Host); err != nil {
			return Options{}, err
		}
	}
	return opts, nil
}

// legacySpec parses the one spelling that addresses an old session:
// <project-id>/<session>. The id is the 12-hex directory name the old layout
// used, so it can be copied from the old state root.
func legacySpec(spec string) (id, session string, err error) {
	parts := strings.Split(strings.TrimSpace(spec), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", usagef("--legacy wants <project-id>/<session>, not %q", spec)
	}
	id, session = parts[0], parts[1]
	if len(id) != 12 {
		return "", "", usagef("--legacy project id %q must be the 12-character directory name from the old state root", id)
	}
	for _, r := range id {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return "", "", usagef("--legacy project id %q is not 12 lowercase hex characters", id)
		}
	}
	if err := validateSession(session); err != nil {
		return "", "", err
	}
	return id, session, nil
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

// validateName is the rule for a new global session name. It is stricter than
// the daemon's because the name is also a directory under the state root on
// case-insensitive filesystems: lowercase ASCII avoids "Work" and "work"
// becoming the same directory without admitting it.
func validateName(name string) error {
	if err := validateSession(name); err != nil {
		return err
	}
	switch {
	case name == "":
		return usagef("a session name is required")
	case len(name) > maxSessionName:
		return usagef("session name %q is %d bytes; the workspace daemon allows at most %d", name, len(name), maxSessionName)
	}
	for i, r := range name {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
		if i > 0 {
			ok = ok || r == '-' || r == '_'
		}
		if !ok {
			return usagef("session name %q may use lowercase letters, digits, '-' and '_', and must start with a letter or digit", name)
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

// namedSocketPath is the global-session rule: the socket is keyed by the state
// root and the session name, never by the directory the command runs in. A
// record keeps the path it was created with, so a changed TMPDIR cannot hide a
// running daemon from status or attach.
func namedSocketPath(getenv func(string) string, home, session, custom, cwd string) (path, dir string, err error) {
	if custom != "" && !filepath.IsAbs(custom) && cwd == "" {
		return "", "", usagef("--socket %q is relative and this session has no start directory to resolve it against", custom)
	}
	return socketPath(getenv, cwd, home, session, custom)
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
	if res.Cwd != "" {
		o.Cwd, o.Project = res.Cwd, res.Cwd
	}
	if res.Config != "" {
		o.ConfigPath = res.Config
	}
	if res.StateDir != "" {
		o.StateDir = res.StateDir
	}
	if res.Log != "" {
		o.LogFile = res.Log
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
