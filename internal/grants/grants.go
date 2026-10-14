// Package grants is the single authorization table: which paths golder may read,
// write, and run side-effect tools in — and for how long.
//
// Why one table. The same question used to be answered by three stores with
// three schemas: per-directory trust in $GOLDER_HOME/trust.json (a map of
// directory to a nullable bool), the sandbox roots in
// ~/.config/golder/permissions.toml (two string arrays), and two package-level
// registries the gates filled as they granted things. None could answer the
// whole question, which is why /trust list and /permissions list each had to
// point at the other. The model here is one row per path:
//
//	path      — cleaned absolute, covers the subtree unless Scope says otherwise
//	allow     — the capabilities this row grants
//	deny      — the capabilities this row refuses
//	lifetime  — remembered (on disk) or session (this process only)
//
// Capabilities are not one axis. Execute is the odd one and the reason the
// model needs three bits rather than a read/write pair:
//
//	Read     — open or list the path
//	Write    — create, change or delete under it (implies Read)
//	Execute  — run side-effect tools with the path as their working directory
//
// Execute is what trust.json always meant. Keeping it separate is what lets a
// migrated trust decision stay exactly as wide as it was: a directory trusted
// for running commands is not thereby a directory whose files may be rewritten
// from anywhere else, and conflating the two is how a permission store silently
// widens on upgrade.
//
// A row may both grant and refuse. That is not a contradiction: writing roots
// and a trust refusal can name the same directory, and the honest reading is
// "may write here, may not execute here". Decisions are per capability and
// most-specific-wins, so a refusal on a child still beats a grant on its
// parent.
//
// The store is safe for concurrent use: grants arrive from a driver goroutine
// (a dialog answer) while run goroutines resolve paths against the table.
package grants

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/getan/golder/internal/permissions"
)

// Capability is one thing a grant may permit or refuse. The zero value permits
// nothing, so a capability set that was never filled in is inert rather than
// silently powerful.
type Capability uint8

const (
	// Read permits opening or listing the path.
	Read Capability = 1 << iota
	// Write permits creating, changing and deleting under the path. It implies
	// Read: writing a file that cannot be read back only produces confusing
	// half-failures, and every store this replaces agreed.
	Write
	// Execute permits running side-effect tools (bash, apply_patch) with this
	// path as their working directory. It is what a directory-level trust
	// decision has always meant, and it is deliberately not implied by Write:
	// being allowed to edit files somewhere is not the same as being allowed to
	// run arbitrary programs there.
	Execute
)

const (
	// ReadWrite is the pair a path grant carries.
	ReadWrite = Read | Write
	// Full is every capability, for callers that mean "no restriction here".
	Full = Read | Write | Execute
)

// Has reports whether c carries every capability in want.
func (c Capability) Has(want Capability) bool { return c&want == want }

// String renders the capability set in the spelling the dialogs and /trust
// print: the letters a person types, in a stable order, e.g. "rw" or "rwe".
func (c Capability) String() string {
	var b strings.Builder
	if c.Has(Read) {
		b.WriteByte('r')
	}
	if c.Has(Write) {
		b.WriteByte('w')
	}
	if c.Has(Execute) {
		b.WriteByte('e')
	}
	return b.String()
}

// ParseCapability reads the capability spellings the command line accepts.
// It accepts combined forms in any order ("rw", "wr", "rwe") plus the long
// names of each bit, so both a person typing `/trust add /x rw` and a config
// file spelling it out are understood.
func ParseCapability(s string) (Capability, bool) {
	text := strings.ToLower(strings.TrimSpace(s))
	if text == "" {
		return 0, false
	}
	switch text {
	case "all", "full":
		return Full, true
	case "read", "readonly", "read-only", "ro":
		return Read, true
	case "write":
		return Write, true
	case "execute", "exec", "run":
		return Execute, true
	case "readwrite", "read-write":
		return ReadWrite, true
	}
	// Letter combinations: every character must be one of the three bits.
	var out Capability
	for _, r := range text {
		switch r {
		case 'r':
			out |= Read
		case 'w':
			out |= Write
		case 'e':
			out |= Execute
		default:
			return 0, false
		}
	}
	if out == 0 {
		return 0, false
	}
	return out, true
}

// normalizeCaps applies the implication every layer shares: granting or
// refusing Write carries Read with it. Doing it in one place means a hand
// edited file, a migrated legacy root and an interactive answer all agree.
func normalizeCaps(c Capability) Capability {
	if c.Has(Write) {
		c |= Read
	}
	return c
}

// storeKey turns a path into the form this table keys rows on: absolute and
// resolved. Keys name LOCATIONS, because that is what an answer is about — the
// user decided about a place, not about a spelling of it.
//
// The distinction is not academic. NormalizePath only cleans, and cleaning does
// not follow symlinks, so on macOS "/var/x" and "/private/var/x" — one place,
// two spellings — would be two unrelated rows. The gate and the tools compare
// locations (they resolve before deciding, because a symlink inside the
// workspace can point outside it), so a row keyed by spelling is a row the
// caller cannot find: the user grants a directory, and the very next call asks
// about it again. That is the failure this function exists to prevent, and it
// is the same mistake in the other direction as the escape the resolved
// comparisons close.
//
// A path that does not exist yet still keys fine: Canonical resolves the
// deepest existing ancestor and re-appends the rest.
func storeKey(p string) (string, error) {
	norm, err := permissions.NormalizePath(p)
	if err != nil {
		return "", err
	}
	return permissions.Canonical(norm), nil
}

// Scope is how wide a grant reaches.
type Scope uint8

const (
	// Subtree covers the path itself and everything under it. It is the default
	// and what a directory grant means.
	Subtree Scope = iota
	// Exact covers only the path itself. It is what an answer about one file
	// produces, so approving a read of /etc/hosts does not hand over /etc.
	Exact
)

// String renders the scope for /trust's listing.
func (s Scope) String() string {
	if s == Exact {
		return "file"
	}
	return "dir"
}

// Lifetime is how long a grant lasts.
type Lifetime string

const (
	// Session grants die with the process and are never written to disk.
	Session Lifetime = "session"
	// Remembered grants are persisted and apply to every later run.
	Remembered Lifetime = "remembered"
)

// Decision is the answer for one (path, capability) question.
type Decision uint8

const (
	// Undecided means nothing in the table speaks to this path and capability.
	Undecided Decision = iota
	// Allowed means the most specific row grants it.
	Allowed
	// Denied means the most specific row refuses it.
	Denied
)

func (d Decision) String() string {
	switch d {
	case Allowed:
		return "allowed"
	case Denied:
		return "denied"
	default:
		return "undecided"
	}
}

// Grant is one row of the table.
type Grant struct {
	// Path is a cleaned absolute path.
	Path string `json:"path"`
	// Allow is what this row grants.
	Allow Capability `json:"-"`
	// Deny is what this row refuses. A row may carry both; see the package
	// comment for why that is not a contradiction.
	Deny Capability `json:"-"`
	// Scope is Subtree or Exact.
	Scope Scope `json:"scope,omitempty"`
	// Lifetime is Session or Remembered.
	Lifetime Lifetime `json:"lifetime"`
	// Source records where a remembered row came from, for /trust's provenance
	// column: a legacy file, a dialog, or a command. Empty for session rows.
	Source string `json:"source,omitempty"`
}

// Capabilities returns the row's effective allow set.
func (g Grant) Capabilities() Capability { return g.Allow }

// String renders one row the way /trust prints it.
func (g Grant) String() string {
	allow := g.Allow.String()
	if allow == "" {
		allow = "-"
	}
	if g.Deny != 0 {
		allow += "/!" + g.Deny.String()
	}
	return fmt.Sprintf("%s  %s  %s", g.Path, allow, g.Lifetime)
}

// rowJSON is the on-disk shape. Capabilities are spelled as readable sets
// rather than a bitmask because the file is meant to be hand-editable, and
// `"allow": 7` is not.
type rowJSON struct {
	Path string `json:"path"`
	// Allow/Deny use the letter spelling: "r", "rw", "rwe".
	Allow    string   `json:"allow,omitempty"`
	Deny     string   `json:"deny,omitempty"`
	Scope    string   `json:"scope,omitempty"`
	Lifetime Lifetime `json:"lifetime"`
	Source   string   `json:"source,omitempty"`
}

type fileJSON struct {
	Version int       `json:"version"`
	Grants  []rowJSON `json:"grants"`
}

// CurrentVersion is the on-disk schema version. A file written by a newer
// golder is refused outright rather than partially understood: reading half a
// permission store is worse than refusing to start with it.
const CurrentVersion = 1

// Store is the unified table.
type Store struct {
	// mu guards every map below. Reads take it for reading, so a run goroutine
	// resolving a path never waits on a dialog's answer beyond the write itself.
	mu sync.RWMutex
	// path is the managed file. Empty disables persistence: remembered requests
	// then degrade to session grants instead of failing, which keeps golder
	// usable when no config directory can be resolved.
	path string
	// saved is the persisted layer.
	saved map[string]Grant
	// session is the process-only layer; never written.
	session map[string]Grant
	// removed records paths deleted from the saved layer in this process. The
	// write path merges with what is on disk so a concurrent golder's grant is
	// not lost, and without this set the merge cannot tell "I never saw this
	// row" from "I just deleted this row" — so a revocation would be silently
	// undone by the next write. Entries die with the process: they describe this
	// session's intent, not a durable fact.
	removed map[string]bool
	// loaded records that the managed file existed, so a caller can tell a
	// first run (worth migrating) from a later one.
	loaded bool
	// warn collects non-fatal problems (an unreadable file, a failed migration
	// write) for the caller to surface once.
	warn []string
}

// Options configure New.
type Options struct {
	// Path overrides the managed file location. Empty uses DefaultPath.
	Path string
	// Getenv overrides the environment lookup used by DefaultPath.
	Getenv func(string) string
	// AllowUnpersisted keeps a store usable when DefaultPath resolves to "".
	// Every remembered request then yields a session grant, and Add reports the
	// degradation through the returned Grant rather than an error.
	AllowUnpersisted bool
}

// DefaultPath returns the managed file:
// $XDG_CONFIG_HOME/golder/grants.json, or ~/.config/golder/grants.json. It sits
// beside permissions.toml and proxy.toml because it is the same kind of thing:
// state golder writes on the user's behalf, in a file the user may still edit.
func DefaultPath(getenv func(string) string) string {
	if getenv == nil {
		getenv = os.Getenv
	}
	base := strings.TrimSpace(getenv("XDG_CONFIG_HOME"))
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "golder", "grants.json")
}

// New opens the table at the managed location. A missing file is not an error:
// the store starts empty and reports Loaded() == false so the caller can decide
// whether to run a migration. A present-but-unreadable or newer-versioned file
// IS an error, because guessing around a broken permission store is how a
// revoked grant comes back.
func New(opts Options) (*Store, error) {
	getenv := opts.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	path := opts.Path
	if path == "" {
		path = DefaultPath(getenv)
	}
	if path == "" && !opts.AllowUnpersisted {
		return nil, fmt.Errorf("grants: cannot locate a settings directory and AllowUnpersisted is not set")
	}
	s := &Store{
		path:    path,
		saved:   map[string]Grant{},
		session: map[string]Grant{},
		removed: map[string]bool{},
	}
	if path == "" {
		return s, nil
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// Loaded reports whether the managed file existed when the store was opened.
// False means this is a first run for the unified table, which is the moment to
// migrate the legacy stores.
func (s *Store) Loaded() bool { return s.loaded }

// Path returns the managed file location, for /trust to print.
func (s *Store) Path() string { return s.path }

// Warnings returns the non-fatal problems seen while opening or saving, so the
// caller can surface them once. They never block a run.
func (s *Store) Warnings() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.warn...)
}

func (s *Store) addWarning(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.warn = append(s.warn, msg)
}

// load reads the managed file into the saved layer.
func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("grants: read %s: %w", s.path, err)
	}
	f, err := parseFile(data)
	if err != nil {
		return fmt.Errorf("grants: %s: %w", s.path, err)
	}
	for _, g := range f {
		s.saved[g.Path] = g
	}
	s.loaded = true
	return nil
}

// parseFile decodes the managed file. An unknown version, an unparsable
// capability or a row with no path is an error rather than a skip: a permission
// store that silently drops rows is worse than one that refuses to load.
func parseFile(data []byte) ([]Grant, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, nil
	}
	var f fileJSON
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if f.Version > CurrentVersion {
		return nil, fmt.Errorf("version %d is newer than this golder understands (%d); upgrade golder or remove the file", f.Version, CurrentVersion)
	}
	out := make([]Grant, 0, len(f.Grants))
	for i, r := range f.Grants {
		g, err := rowFromJSON(r)
		if err != nil {
			return nil, fmt.Errorf("grant %d: %w", i, err)
		}
		out = append(out, g)
	}
	return out, nil
}

func rowFromJSON(r rowJSON) (Grant, error) {
	if strings.TrimSpace(r.Path) == "" {
		return Grant{}, fmt.Errorf("missing path")
	}
	norm, err := storeKey(r.Path)
	if err != nil {
		return Grant{}, fmt.Errorf("path %q: %w", r.Path, err)
	}
	allow, err := capsFromString(r.Allow)
	if err != nil {
		return Grant{}, fmt.Errorf("allow %q: %w", r.Allow, err)
	}
	deny, err := capsFromString(r.Deny)
	if err != nil {
		return Grant{}, fmt.Errorf("deny %q: %w", r.Deny, err)
	}
	if allow == 0 && deny == 0 {
		return Grant{}, fmt.Errorf("row for %s grants and refuses nothing", norm)
	}
	scope := Subtree
	switch strings.ToLower(strings.TrimSpace(r.Scope)) {
	case "", "dir", "subtree", "directory":
		scope = Subtree
	case "file", "exact":
		scope = Exact
	default:
		return Grant{}, fmt.Errorf("unknown scope %q", r.Scope)
	}
	life := r.Lifetime
	if life == "" {
		life = Remembered
	}
	if life != Remembered {
		// A session row in the file would be a lie: the next process would load
		// it as something longer-lived than the writer intended.
		return Grant{}, fmt.Errorf("lifetime %q cannot be persisted (only %q is written)", life, Remembered)
	}
	return Grant{
		Path:     norm,
		Allow:    normalizeCaps(allow),
		Deny:     normalizeCaps(deny),
		Scope:    scope,
		Lifetime: Remembered,
		Source:   r.Source,
	}, nil
}

// capsFromString parses a capability set, rejecting an unknown letter instead
// of ignoring it: silently dropping "e" from "rwe" would narrow a grant the
// user wrote, and silently dropping a typo could widen one.
func capsFromString(s string) (Capability, error) {
	text := strings.TrimSpace(s)
	if text == "" {
		return 0, nil
	}
	c, ok := ParseCapability(text)
	if !ok {
		return 0, fmt.Errorf("unrecognized capability set")
	}
	return c, nil
}

// Add records a grant. A remembered grant is written to disk before it is
// reported as remembered: an answer the user gave in order to stop being asked
// must survive a restart, so when the write fails the grant is kept for this
// session and the returned value says Session, letting the caller tell the
// truth instead of promising persistence it did not achieve.
//
// rows are merged into the existing row for the path: a later grant narrows or
// widens the same path rather than shadowing it, so `/trust add /x r` after
// `/trust add /x w` leaves /x as "rw" instead of whichever row won by ordering.
func (s *Store) Add(g Grant) (Grant, error) {
	if strings.TrimSpace(g.Path) == "" {
		return Grant{}, fmt.Errorf("grants: empty path")
	}
	norm, err := storeKey(g.Path)
	if err != nil {
		return Grant{}, err
	}
	g.Path = norm
	g.Allow = normalizeCaps(g.Allow)
	g.Deny = normalizeCaps(g.Deny)
	if g.Allow == 0 && g.Deny == 0 {
		return Grant{}, fmt.Errorf("grants: nothing to grant or refuse for %s", norm)
	}
	// An unstated lifetime means the durable one. Add is what a caller reaches
	// for to record a decision, and the zero value silently meaning "this
	// session only" would make every dialog answer evaporate on restart — the
	// one outcome a permission store must never produce. A caller that wants
	// the shorter life says Session.
	if g.Lifetime == "" {
		g.Lifetime = Remembered
	}
	if g.Lifetime != Remembered {
		g.Lifetime = Session
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if g.Lifetime == Session {
		s.session[norm] = mergeGrant(s.session[norm], g)
		return s.session[norm], nil
	}
	if s.path == "" {
		// Nowhere to persist: a working session grant beats a failure.
		g.Lifetime = Session
		s.session[norm] = mergeGrant(s.session[norm], g)
		return s.session[norm], nil
	}
	prev, had := s.saved[norm]
	s.saved[norm] = mergeGrant(prev, g)
	// Adding a path clears any tombstone for it: the user's latest answer is
	// the one that counts.
	delete(s.removed, norm)
	if err := s.saveLocked(); err != nil {
		// Roll the in-memory row back too, so the table never claims a grant
		// the file does not have.
		if had {
			s.saved[norm] = prev
		} else {
			delete(s.saved, norm)
		}
		g.Lifetime = Session
		s.session[norm] = mergeGrant(s.session[norm], g)
		s.warn = append(s.warn, fmt.Sprintf("grants: could not persist %s (%v); kept for this session", norm, err))
		return s.session[norm], nil
	}
	return s.saved[norm], nil
}

// mergeGrant folds a new row into an existing one. A caller reaching Add always
// knows the scope it means, so the newer row's scope and source win, which is
// how re-adding a path narrows a subtree grant to a single file.
func mergeGrant(old, add Grant) Grant {
	out := old
	if add.Path != "" {
		out.Path = add.Path
	}
	out.Allow |= add.Allow
	out.Deny |= add.Deny
	// A capability that is now granted is no longer refused: the newer answer
	// wins on the bits it speaks to, which is what "approve this read" means
	// after an earlier refusal.
	out.Deny &^= add.Allow
	out.Allow &^= add.Deny
	out.Scope = add.Scope
	if add.Source != "" {
		out.Source = add.Source
	}
	if add.Lifetime != "" {
		out.Lifetime = add.Lifetime
	}
	return out
}

// Refuse records an explicit refusal. It exists as its own entry point because
// "no" is a decision worth persisting: without it, answering no to a directory
// would be forgotten and the next call would ask again.
func (s *Store) Refuse(path string, caps Capability, life Lifetime) (Grant, error) {
	if caps == 0 {
		caps = Execute
	}
	return s.Add(Grant{Path: path, Deny: caps, Lifetime: life})
}

// Check answers one (path, capability) question by most-specific-wins: the walk
// starts at the path itself and moves toward the root, and the first layer at
// the first level that speaks to this capability decides it. A refusal recorded
// for a child therefore still beats a grant inherited from its parent.
//
// Layers rank session, then remembered. Session ranks first so an answer given
// in this run takes effect immediately rather than being masked by an older
// saved row for the same directory.
func (s *Store) Check(path string, cap Capability) Verdict {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.checkLocked(path, cap)
}

// Verdict is the outcome of Check.
type Verdict struct {
	// Decision is Allowed, Denied or Undecided.
	Decision Decision
	// By is the path of the row that decided it, "" when undecided. It is the
	// path the user would have to revoke to change the answer.
	By string
	// Grant is the deciding row.
	Grant Grant
}

// Allows is the boolean form of Check, for callers that only gate.
func (s *Store) Allows(path string, cap Capability) bool {
	return s.Check(path, cap).Decision == Allowed
}

func (s *Store) checkLocked(path string, cap Capability) Verdict {
	if strings.TrimSpace(path) == "" || cap == 0 {
		return Verdict{}
	}
	// The query is resolved the same way the keys are (storeKey), so a grant
	// recorded under one spelling answers a call made under another.
	clean, err := storeKey(path)
	if err != nil {
		return Verdict{}
	}
	for dir := clean; ; dir = filepath.Dir(dir) {
		for _, layer := range []map[string]Grant{s.session, s.saved} {
			g, ok := findRow(layer, dir, clean)
			if !ok {
				continue
			}
			switch {
			case g.Allow.Has(cap):
				return Verdict{Decision: Allowed, By: g.Path, Grant: g}
			case g.Deny.Has(cap):
				return Verdict{Decision: Denied, By: g.Path, Grant: g}
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return Verdict{}
		}
	}
}

// findRow looks up one level: the exact key first, then a subtree row that
// covers the target. An Exact row only matches its own path, which is what
// keeps "allow this one read" from turning into the whole directory.
func findRow(layer map[string]Grant, level, target string) (Grant, bool) {
	if g, ok := layer[level]; ok {
		if g.Scope == Subtree || g.Path == target {
			return g, true
		}
	}
	return Grant{}, false
}

// Entries returns every row: remembered, then session, each sorted by path.
// /trust prints this; the layers are separate because removing a row means
// different things in each.
func (s *Store) Entries() Entries {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Entries{
		Remembered: sortedGrants(s.saved),
		Session:    sortedGrants(s.session),
	}
}

// Entries is the whole table, in the two layers /trust reports.
type Entries struct {
	// Remembered is the managed store.
	Remembered []Grant
	// Session is this process only.
	Session []Grant
}

// All returns every row from every layer, for a caller that wants one list.
func (e Entries) All() []Grant {
	out := make([]Grant, 0, len(e.Remembered)+len(e.Session))
	out = append(out, e.Remembered...)
	out = append(out, e.Session...)
	return out
}

func sortedGrants(m map[string]Grant) []Grant {
	out := make([]Grant, 0, len(m))
	for _, g := range m {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// SessionOnly reports whether path is granted only for this process, which the
// dialog uses to offer "remember this" without re-asking a question already
// answered durably.
func (s *Store) SessionOnly(path string, cap Capability) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v := s.checkLocked(path, cap)
	if v.Decision != Allowed {
		return false
	}
	clean, err := storeKey(path)
	if err != nil {
		return true
	}
	for dir := clean; ; dir = filepath.Dir(dir) {
		if g, ok := findRow(s.saved, dir, clean); ok && g.Allow.Has(cap) {
			return false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return true
		}
	}
}

// Remove revokes a path from the layers the caller can edit: the session layer
// always, and the managed file when it holds a row.
func (s *Store) Remove(path string) (Removal, error) {
	norm, err := storeKey(path)
	if err != nil {
		return Removal{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out Removal
	if _, ok := s.session[norm]; ok {
		delete(s.session, norm)
		out.Session = true
	}
	prev, ok := s.saved[norm]
	if !ok {
		return out, nil
	}
	delete(s.saved, norm)
	out.Remembered = true
	// Record the intent so the merge in saveLocked cannot resurrect the row
	// from the copy still on disk.
	s.removed[norm] = true
	if s.path == "" {
		return out, nil
	}
	if err := s.saveLocked(); err != nil {
		s.saved[norm] = prev
		delete(s.removed, norm)
		return out, err
	}
	out.Persisted = true
	return out, nil
}

// Removal reports which layers a Remove touched.
type Removal struct {
	// Session and Remembered each report whether that layer held the row.
	Session    bool
	Remembered bool
	// Persisted reports that the managed file was rewritten.
	Persisted bool
}

// Touched reports whether any layer held the row.
func (r Removal) Touched() bool { return r.Session || r.Remembered }

// saveLocked writes the managed file atomically. Callers hold the write lock.
//
// The write is serialized against other golder processes by a lock file beside
// the table (see lock_unix.go). Without it two processes that each read the
// table, added a grant and wrote it back would keep only the later write, so a
// grant one of them recorded would vanish — the exact failure a permission
// store cannot afford.
func (s *Store) saveLocked() error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(s.path), err)
	}
	unlock, err := lockFile(s.path + ".lock")
	if err != nil {
		return fmt.Errorf("lock: %w", err)
	}
	defer unlock()

	// Re-read under the lock and merge, so a grant another process wrote while
	// this one was deciding is not overwritten by our older snapshot. Rows we
	// hold win on a path we touched, rows we deleted stay deleted, and rows we
	// never saw survive.
	if onDisk, err := s.readSaved(); err != nil {
		s.warn = append(s.warn, fmt.Sprintf("grants: could not re-read %s (%v); writing our snapshot", s.path, err))
	} else {
		for path, g := range onDisk {
			if s.removed[path] {
				continue
			}
			if _, mine := s.saved[path]; !mine {
				s.saved[path] = g
			}
		}
	}

	f := fileJSON{Version: CurrentVersion}
	for _, g := range s.saved {
		f.Grants = append(f.Grants, rowToJSON(g))
	}
	sort.Slice(f.Grants, func(i, j int) bool { return f.Grants[i].Path < f.Grants[j].Path })
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	b = append(b, '\n')

	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".grants-*.json")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("chmod: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// readSaved reads the current file without taking the lock (the caller holds
// it) and without disturbing the in-memory table.
func (s *Store) readSaved() (map[string]Grant, error) {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := parseFile(data)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Grant, len(rows))
	for _, g := range rows {
		out[g.Path] = g
	}
	return out, nil
}

func rowToJSON(g Grant) rowJSON {
	return rowJSON{
		Path:     g.Path,
		Allow:    g.Allow.String(),
		Deny:     g.Deny.String(),
		Scope:    scopeString(g.Scope),
		Lifetime: Remembered,
		Source:   g.Source,
	}
}

func scopeString(s Scope) string {
	if s == Exact {
		return "file"
	}
	return ""
}
