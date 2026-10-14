// Package golderhome resolves golder's state root, in one place.
//
// Every path golder writes on the user's behalf lives under one directory:
// $GOLDER_HOME when set, else ~/.golder. It used to be two — the state
// directory held the session store, history, trust and the credential file,
// while ~/.config/golder (or $XDG_CONFIG_HOME) held config.toml, proxy.toml and
// grants.json — and two directories meant two answers to "where does golder
// keep this", resolved independently in a dozen packages. That is how
// grants.json, the one file that decides what the agent may read and write,
// came to sit outside the sandbox's own protected set while trust.json sat
// inside it.
//
// One root also makes the sandbox's job tractable: ProtectedGolderPaths denies
// the directory, so a file added here later is protected without anyone
// remembering to add it to a list.
//
// The package is deliberately leaf: stdlib only, no config, no logging. It is
// imported by the gates, the providers and the CLI alike, and anything more
// would make one of those imports a cycle.
package golderhome

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnvHome overrides the state root. Set, it is used verbatim.
const EnvHome = "GOLDER_HOME"

// Names under the root. They are constants rather than literals at the call
// sites for two reasons: a rename is one edit, and the audit question "what
// does golder write" is answered by reading this block.
const (
	ConfigFile      = "config.toml"
	ProxyFile       = "proxy.toml"
	GrantsFile      = "grants.json"
	TrustFile       = "trust.json"
	CredentialsFile = ".credentials.yaml"
	HistoryFile     = "history.jsonl"
	BtwFile         = "btw.json"
	ConfigJSONFile  = "config.json"
	UpdateCheckFile = "update-check.json"

	SessionsDir = "sessions"
	MemoryDir   = "memory"
	PluginsDir  = "plugins"
	CommandsDir = "commands"
	PromptsDir  = "prompts"
)

// Dir returns the state root: $GOLDER_HOME, else ~/.golder. An unresolvable
// home returns "", which callers treat as "this feature is unavailable" — never
// as a reason to fall back to the working directory, where a state file would
// be a surprise in the user's repository.
func Dir() string { return DirFrom(os.Getenv) }

// DirFrom is Dir with an injectable environment, for tests and for callers
// that already carry a getenv (the grants store does).
func DirFrom(getenv func(string) string) string {
	if getenv == nil {
		getenv = os.Getenv
	}
	if dir := strings.TrimSpace(getenv(EnvHome)); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".golder")
}

// File returns the path of name under the root, or "" when the root cannot be
// resolved.
func File(name string) string { return FileFrom(os.Getenv, name) }

// FileFrom is File with an injectable environment.
func FileFrom(getenv func(string) string, name string) string {
	dir := DirFrom(getenv)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, name)
}

// legacySubdir is the directory the pre-move release kept its config in:
// $XDG_CONFIG_HOME/golder, or ~/.config/golder.
const legacySubdir = "golder"

// LegacyFile returns where a release before the one-root change kept name. It
// exists for exactly two readers — the one-time move below and the authorization
// migration, which has to find the retired permissions.toml — and for nothing
// else: a path that both the old and the new code can write is a path that
// splits the truth again.
func LegacyFile(name string) string { return LegacyFileFrom(os.Getenv, name) }

// LegacyFileFrom is LegacyFile with an injectable environment.
func LegacyFileFrom(getenv func(string) string, name string) string {
	base := legacyDir(getenv)
	if base == "" {
		return ""
	}
	return filepath.Join(base, name)
}

func legacyDir(getenv func(string) string) string {
	if getenv == nil {
		getenv = os.Getenv
	}
	if base := strings.TrimSpace(getenv("XDG_CONFIG_HOME")); base != "" {
		return filepath.Join(base, legacySubdir)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config", legacySubdir)
}

// migrateNames are the files that moved. permissions.toml is deliberately not
// among them: it is retired, and its only remaining reader is the authorization
// migration, which looks for it where it was written (see LegacyFile).
var migrateNames = []string{ConfigFile, ProxyFile, GrantsFile}

// MigrateLegacy moves the files that used to live under ~/.config/golder into
// the state root, once. It is called at startup, before anything reads them, and
// it is idempotent: a destination that already exists wins, so a user who has
// since edited the new file is never overwritten by the old one, and a second
// launch finds nothing to do.
//
// It does not run when $GOLDER_HOME is set: that variable says where the state
// is, and a home-relative legacy directory has nothing to do with it.
//
// announce, when non-nil, is told about each move in the user's own terms —
// silently relocating a file someone hand-edits would leave them editing a
// path that no longer does anything.
func MigrateLegacy(getenv func(string) string, announce func(string)) {
	if getenv == nil {
		getenv = os.Getenv
	}
	if strings.TrimSpace(getenv(EnvHome)) != "" {
		return
	}
	for _, name := range migrateNames {
		dest, src := FileFrom(getenv, name), LegacyFileFrom(getenv, name)
		if dest == "" || src == "" || dest == src {
			continue
		}
		if _, err := os.Stat(dest); err == nil {
			continue // already moved, or the user made a new one
		}
		if _, err := os.Stat(src); err != nil {
			continue // nothing to move
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			report(announce, fmt.Sprintf("cannot create %s: %v; still using %s", filepath.Dir(dest), err, src))
			continue
		}
		if err := os.Rename(src, dest); err != nil {
			// A rename across filesystems (a home split over volumes, a
			// bind-mounted ~/.config) fails with EXDEV; copy instead, and leave
			// the original in place rather than removing a file we could not
			// move atomically.
			data, rerr := os.ReadFile(src)
			if rerr != nil || os.WriteFile(dest, data, 0o600) != nil {
				report(announce, fmt.Sprintf("cannot move %s to %s; still using %s", src, dest, src))
				continue
			}
		}
		if name == GrantsFile {
			// The table's lock file is not state: it is recreated beside
			// whichever file is live, and a stale one in the old directory would
			// only confuse a reader.
			_ = os.Remove(src + ".lock")
		}
		report(announce, fmt.Sprintf("moved %s to %s", src, dest))
	}
}

func report(announce func(string), msg string) {
	if announce != nil {
		announce(msg)
	}
}
