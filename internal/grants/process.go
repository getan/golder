package grants

// The process-level table.
//
// One table per process, for the same reason permissions.ReadRoots is
// package-level: every tool, every gate and every sub-agent thread must see the
// same answer, and a grant the user just gave has to apply to the next call
// without rewiring anything. It is initialized once at startup (before any run)
// and read thereafter.
//
// Initialization is explicit rather than lazy on purpose. The table reads and
// migrates files; doing that from inside a gate would put disk I/O on the path
// of every tool call, and a migration failure would surface as a tool error
// rather than as the startup warning it is.

import (
	"errors"
	"os"
	"sync"
)

// errNoTable reports a grant that had nowhere to live. The grant is still
// returned (as a session grant) so a caller can act on it, but the error tells
// the truth about persistence.
var errNoTable = errors.New("grants: no process table is open")

var process = struct {
	mu sync.RWMutex
	s  *Store
}{}

// Process returns the process table, or nil when it was never initialized (a
// caller that forgot Open, or a test that did not need one). A nil return is
// not an error: callers treat it as "no grants recorded", which is exactly what
// an empty table means.
func Process() *Store {
	process.mu.RLock()
	defer process.mu.RUnlock()
	return process.s
}

// Open initializes the process table: it loads the managed file and, on a first
// run, folds the legacy stores into it. It reports the warnings a caller should
// print once (an unreadable legacy file, a failed migration write); they never
// abort a run, because losing an old grant is an inconvenience while refusing
// to start is not.
//
// Calling it twice replaces the table, which is what a test wants and what a
// second call in production would be a bug about — hence the single documented
// call site in main.
func Open(getenv func(string) string) (warnings []string, err error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	s, err := New(Options{Getenv: getenv, AllowUnpersisted: true})
	if err != nil {
		return nil, err
	}
	warnings = append(warnings, s.Warnings()...)
	if !s.Loaded() {
		// A first run: fold in whatever the three older stores held. The call
		// is a no-op when there was nothing to import.
		res, merr := s.Migrate(LegacyPathsFor(getenv))
		if merr != nil {
			return warnings, merr
		}
		warnings = append(warnings, res.Warnings...)
	}
	process.mu.Lock()
	process.s = s
	process.mu.Unlock()
	return warnings, nil
}

// SetProcess installs a table directly. It exists for tests and for a caller
// that built a table it wants to be the process one.
func SetProcess(s *Store) {
	process.mu.Lock()
	process.s = s
	process.mu.Unlock()
}

// Check consults the process table. With no table every path is Undecided,
// which is the pre-table behavior: the caller asks.
func Check(path string, cap Capability) Verdict {
	if s := Process(); s != nil {
		return s.Check(path, cap)
	}
	return Verdict{}
}

// Allows consults the process table, returning false when there is none.
func Allows(path string, cap Capability) bool {
	return Check(path, cap).Decision == Allowed
}

// Add records a grant in the process table and reports whether it could be
// persisted (Remembered) or only held for this session (Session). A grant with
// no table to record it in is dropped, and the caller is told.
func Add(g Grant) (Grant, error) {
	s := Process()
	if s == nil {
		g.Lifetime = Session
		return g, errNoTable
	}
	return s.Add(g)
}

// Refuse records a refusal in the process table.
func Refuse(path string, caps Capability, life Lifetime) (Grant, error) {
	s := Process()
	if s == nil {
		return Grant{}, errNoTable
	}
	return s.Refuse(path, caps, life)
}

// ReadRoots returns the subtree paths the table grants read access to, which is
// the form the sandbox and the read-root registry want.
//
// It exists so the table can be the single source of truth without the legacy
// registries reading it directly: a caller that used to take
// permissions.ReadRoots() takes this instead, and the conversion (subtree-only,
// read-capable) happens in one place rather than at each call site.
func ReadRoots() []string {
	s := Process()
	if s == nil {
		return nil
	}
	var out []string
	for _, g := range s.Entries().All() {
		if g.Scope == Exact || !g.Allow.Has(Read) {
			continue
		}
		out = append(out, g.Path)
	}
	return out
}

// WritableRoots returns the subtree paths the table grants write access to: the
// form the sandbox's writable registry wants.
func WritableRoots() []string {
	s := Process()
	if s == nil {
		return nil
	}
	var out []string
	for _, g := range s.Entries().All() {
		if g.Scope == Exact || !g.Allow.Has(Write) {
			continue
		}
		out = append(out, g.Path)
	}
	return out
}
