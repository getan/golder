// Package trust persists per-directory trust decisions so golder can avoid running
// side-effect tools (bash/write/edit) in directories the user has not trusted
// (US-018, #134). Decisions are stored as a JSON map of directory path to a
// nullable boolean: true = trusted, false = untrusted, null/absent = undecided.
//
// The package is intentionally free of any REPL or tool-execution concerns: it
// only loads, queries, and persists decisions. The interactive prompt and the
// permission-hook integration live in cmd/golder.
package trust

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/getan/golder/internal/grants"
)

// Decision is the tri-state trust value for a directory.
type Decision int

const (
	// Undecided means no decision is saved for the directory (or an explicit
	// null entry). The caller should prompt the user and treat side-effect
	// tools as requiring confirmation.
	Undecided Decision = iota
	// Trusted means side-effect tools may run without confirmation.
	Trusted
	// Untrusted means side-effect tools require confirmation.
	Untrusted
)

// String returns a human-readable label for the decision.
func (d Decision) String() string {
	switch d {
	case Trusted:
		return "trusted"
	case Untrusted:
		return "untrusted"
	default:
		return "undecided"
	}
}

// decisionFromBool maps a saved nullable boolean to a Decision. A nil pointer
// (JSON null, or an absent entry) is Undecided.
func decisionFromBool(b *bool) Decision {
	if b == nil {
		return Undecided
	}
	if *b {
		return Trusted
	}
	return Untrusted
}

// boolFromDecision maps a Decision to the nullable boolean persisted to disk.
// Undecided maps to nil so the JSON value is null, preserving the
// "path -> bool|null" schema even for an explicitly-recorded undecided entry.
func boolFromDecision(d Decision) *bool {
	switch d {
	case Trusted:
		v := true
		return &v
	case Untrusted:
		v := false
		return &v
	default:
		return nil
	}
}

// Result is the outcome of a nearest-decision lookup.
type Result struct {
	// Decision is the nearest saved decision, or Undecided when none is found.
	Decision Decision
	// Path is the directory whose saved decision applies (the nearest ancestor
	// of cwd with an entry, inclusive of cwd itself). Empty when no entry was
	// found anywhere up the tree.
	Path string
	// Found reports whether any entry (true/false/null) existed for cwd or an
	// ancestor. When false, Decision is Undecided and the caller should prompt
	// the user for a fresh decision.
	Found bool
}

// Manager loads and persists trust decisions to a JSON file (path -> *bool).
// The zero value is not usable; construct with NewManager. It is safe for
// concurrent use: every method takes the manager mutex.
type Manager struct {
	path string
	mu   sync.Mutex
	// StrictReads extends the trust gate to data-ingestion tools (read, grep,
	// find, ls, webfetch, websearch) in untrusted directories. Fork default is
	// true (security first): a rejected directory prompts for every file and
	// network read, not just writes. Pure in-memory tools (todo, schedule_*,
	// goal_*, memory_*) are never gated. Escapes: --approve, /trust once|on,
	// or the prompt's "always" answer (all grant session trust).
	StrictReads bool
	data        map[string]*bool
	// session marks directories trusted for the current process only ("just
	// once"). It is never persisted and is consulted by IsTrusted before the
	// on-disk data, so a one-shot grant takes effect immediately.
	session map[string]bool
	// sessionReads marks directories whose local read tools were allowed for
	// the current process only ("allow reads here"): reads stop asking there,
	// while side-effect tools and network reads keep doing so. Like session it
	// is never persisted, and ClearSessionTrust revokes it alongside session
	// trust so "/trust off" cannot leave half a grant standing.
	sessionReads map[string]bool
}

// DefaultPath returns the trust file location: $GOLDER_HOME/trust.json, or
// ~/.golder/trust.json when GOLDER_HOME is unset. It returns "" when the home
// directory cannot be resolved and no override is set, so a caller can treat
// trust as disabled rather than guessing a path.
func DefaultPath() string {
	if dir := os.Getenv("GOLDER_HOME"); dir != "" {
		return filepath.Join(dir, "trust.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".golder", "trust.json")
}

// NewManager loads the trust file at path. A missing file is not an error: the
// manager starts empty and the file is created lazily on the first SetDecision
// / Forget. A present-but-malformed file is a hard error so a corrupted trust
// store is surfaced rather than silently overwritten.
func NewManager(path string) (*Manager, error) {
	m := &Manager{
		path:         path,
		StrictReads:  true,
		data:         make(map[string]*bool),
		session:      make(map[string]bool),
		sessionReads: make(map[string]bool),
	}
	if path == "" {
		return m, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return m, nil
		}
		return nil, fmt.Errorf("trust: read %s: %w", path, err)
	}
	if len(data) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(data, &m.data); err != nil {
		return nil, fmt.Errorf("trust: parse %s: %w", path, err)
	}
	if m.data == nil {
		m.data = make(map[string]*bool)
	}
	return m, nil
}

// walkUp returns the directory chain from cwd (inclusive) up to the filesystem
// root, in nearest-first order. Paths are cleaned. An empty cwd yields no
// entries.
func walkUp(cwd string) []string {
	cwd = filepath.Clean(cwd)
	if cwd == "" || cwd == "." {
		return nil
	}
	var dirs []string
	cur := cwd
	for {
		dirs = append(dirs, cur)
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return dirs
}

// nearestLocked computes the nearest saved decision for cwd without taking the
// mutex, so it can be reused inside already-locked methods.
func (m *Manager) nearestLocked(cwd string) Result {
	for _, dir := range walkUp(cwd) {
		if v, ok := m.data[dir]; ok {
			return Result{Decision: decisionFromBool(v), Path: dir, Found: true}
		}
	}
	return Result{Decision: Undecided, Found: false}
}

// NearestTrustDecision walks up from cwd (inclusive) to the filesystem root and
// returns the nearest saved decision. When no entry is found anywhere up the
// tree it returns {Decision: Undecided, Found: false} so the caller knows to
// prompt the user.
func (m *Manager) NearestTrustDecision(cwd string) Result {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.nearestLocked(cwd)
}

// IsTrusted reports whether cwd is trusted for side-effect execution. It
// returns true when the nearest persisted decision is Trusted, or when cwd (or
// an ancestor) was granted session trust via SetSessionTrust. Everything else
// (Untrusted, Undecided, or no entry) returns false, meaning side-effect tools
// require confirmation.
func (m *Manager) IsTrusted(cwd string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, dir := range walkUp(cwd) {
		if m.session[dir] {
			return true
		}
	}
	return m.nearestLocked(cwd).Decision == Trusted
}

// SetDecision persists a decision for dir. Trusted and Untrusted write true and
// false respectively; Undecided writes an explicit null entry (recorded but
// undecided, distinct from a forgotten/absent entry). The directory is created
// lazily when the trust file is first written.
func (m *Manager) SetDecision(dir string, dec Decision) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	dir = filepath.Clean(dir)
	m.data[dir] = boolFromDecision(dec)
	m.mirror(dir, dec)
	return m.saveLocked()
}

// mirror records a decision in the unified authorization table.
//
// It is called from every write path here rather than from each caller, because
// the callers are many (the launch picker in both front-ends, /trust on|off|once,
// the gate's dialog answers) and a decision that reached only one of the two
// stores would make the table disagree with the gate that enforces it — the
// worst of both, since /trust would then list something the gate ignores, or
// hide something it obeys.
//
// The capability is Execute, deliberately: this store has only ever answered
// "may side-effect tools run here", which is exactly Execute. It never implies
// Read or Write, so trusting a directory does not silently hand out file grants
// the user never gave — the same reasoning that shapes the legacy migration.
//
// The caller must hold m.mu.
func (m *Manager) mirror(dir string, dec Decision) {
	if m == nil {
		return
	}
	switch dec {
	case Trusted:
		_, _ = grants.Add(grants.Grant{
			Path: dir, Allow: grants.Execute, Scope: grants.Subtree,
			Lifetime: grants.Remembered, Source: "trust store",
		})
	case Untrusted:
		// A refusal is a decision, so it is recorded as one. Without it the
		// table would say "undecided" and the next gate would ask a question
		// the user already answered.
		_, _ = grants.Refuse(dir, grants.Execute, grants.Remembered)
	default:
		// Undecided (an explicit null) records nothing: it said "no decision
		// yet", which is what an absent row already means.
	}
}

// SetSessionTrust grants trust for dir for the current process only. It is not
// persisted: a future golder launch re-prompts. Used by the "just once" REPL
// choice and by the confirmation prompt's "always" response.
func (m *Manager) SetSessionTrust(dir string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	dir = filepath.Clean(dir)
	m.session[dir] = true
	_, _ = grants.Add(grants.Grant{
		Path: dir, Allow: grants.Execute, Scope: grants.Subtree,
		Lifetime: grants.Session, Source: "trust (this session)",
	})
}

// SetSessionReads grants local reads for dir for the current process only: the
// read tools that read the folder's own files (LocalReadTools) stop asking in
// dir and its ancestors, while side-effect tools and network reads keep asking.
// It is not persisted: a future golder launch re-prompts. Used by the trust
// prompt's "reads here" answer.
func (m *Manager) SetSessionReads(dir string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	dir = filepath.Clean(dir)
	m.sessionReads[dir] = true
	// Read, not Execute: the narrower answer must stay narrower, or a directory
	// the user allowed reads in would silently start running commands.
	_, _ = grants.Add(grants.Grant{
		Path: dir, Allow: grants.Read, Scope: grants.Subtree,
		Lifetime: grants.Session, Source: "reads (this session)",
	})
}

// ReadsTrusted reports whether dir (or an ancestor) was granted session reads
// via SetSessionReads. It says nothing about side-effect trust - IsTrusted
// answers that - and network reads are not grantable per directory at all. A
// nil manager grants nothing.
func (m *Manager) ReadsTrusted(cwd string) bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, dir := range walkUp(cwd) {
		if m.sessionReads[dir] {
			return true
		}
	}
	return false
}

// ClearSessionTrust revokes any session trust granted for dir or an ancestor,
// so a subsequent IsTrusted reflects only the persisted decision. The session
// reads grant is revoked with it: answering "no" to a directory must not leave
// its reads half standing. It does not touch the on-disk store. Used by
// "/trust off" so an active session grant (from a prior "always") does not
// override a freshly-persisted Untrusted entry - IsTrusted checks session
// before persisted, so without this clear the "off" command would be
// ineffective until restart.
func (m *Manager) ClearSessionTrust(dir string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range walkUp(dir) {
		delete(m.session, d)
		delete(m.sessionReads, d)
	}
}

// Forget removes any saved decision for dir (both true/false and an explicit
// null entry), so the directory is treated as undecided on the next lookup.
func (m *Manager) Forget(dir string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	dir = filepath.Clean(dir)
	delete(m.data, dir)
	return m.saveLocked()
}

// TrustEntry is one saved decision as /trust list reports it, with the provenance
// that decides what revoking it takes.
type TrustEntry struct {
	Path     string
	Decision Decision
}

// Entries returns every saved decision, sorted by path, plus the two
// session-only grants: directories trusted for this process, and directories
// whose local reads were allowed for this process. /trust status answers "what
// applies to THIS directory" by walking up; this answers "what is in the store",
// which is the question a user auditing their grants is actually asking.
//
// The walk is deliberately absent: entries are the recorded keys, so a decision
// saved for a parent is listed once, under its own directory, instead of
// repeated for every child that inherits it.
func (m *Manager) Entries() (saved []TrustEntry, sessionTrust, sessionReads []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	saved = make([]TrustEntry, 0, len(m.data))
	for path, v := range m.data {
		saved = append(saved, TrustEntry{Path: path, Decision: decisionFromBool(v)})
	}
	slices.SortFunc(saved, func(a, b TrustEntry) int { return strings.Compare(a.Path, b.Path) })
	for dir := range m.session {
		sessionTrust = append(sessionTrust, dir)
	}
	for dir := range m.sessionReads {
		sessionReads = append(sessionReads, dir)
	}
	slices.Sort(sessionTrust)
	slices.Sort(sessionReads)
	return saved, sessionTrust, sessionReads
}

// Path returns the trust file this manager reads and writes ("" when the store
// is disabled), so a report can name the file it is describing.
func (m *Manager) Path() string {
	if m == nil {
		return ""
	}
	return m.path
}

// DecisionFor returns the raw saved value for a single path (nil when absent or
// null). It is the exact-path lookup (no walk), used by /trust status to show
// what is stored for the current directory itself.
func (m *Manager) DecisionFor(dir string) (Decision, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[filepath.Clean(dir)]
	if !ok {
		return Undecided, false
	}
	return decisionFromBool(v), true
}

// saveLocked writes the trust map to disk atomically so a crash mid-write
// cannot leave a truncated store. json.Marshal sorts map keys, so the output is
// stable and diff-friendly; a nil *bool marshals as JSON null, preserving the
// "path -> bool|null" schema. The temp file is created with os.CreateTemp
// (mode 0o600, process-unique name) so two concurrent golder processes writing
// the shared store cannot clobber each other's temp file before the rename.
// The caller must hold m.mu.
func (m *Manager) saveLocked() error {
	if m.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o700); err != nil {
		return fmt.Errorf("trust: create dir: %w", err)
	}
	b, err := json.Marshal(m.data)
	if err != nil {
		return fmt.Errorf("trust: marshal: %w", err)
	}
	b = append(b, '\n')
	dir := filepath.Dir(m.path)
	f, err := os.CreateTemp(dir, filepath.Base(m.path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("trust: create temp file: %w", err)
	}
	tmpPath := f.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := f.Write(b); err != nil {
		f.Close()
		cleanup()
		return fmt.Errorf("trust: write %s: %w", tmpPath, err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return fmt.Errorf("trust: close %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, m.path); err != nil {
		cleanup()
		return fmt.Errorf("trust: rename %s: %w", m.path, err)
	}
	return nil
}
