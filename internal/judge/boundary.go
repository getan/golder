package judge

// The boundary question: stepping outside the workspace needs the user's
// answer, for reading and for writing alike.
//
// The workspace boundary is the directory the user opened. Crossing it is a
// question of consent rather than of risk, which is why it is settled by the
// consent gate (consent.go) rather than by a risk tier, and why no approval mode
// waives it: read-only, ask, auto and full-access all ask before a file outside
// the workspace is touched. full-access turns off review and sandboxing, not the
// user's say over where data comes from or goes, and a run with no prompt
// available (headless, a sub-agent thread with no UI) fails closed with guidance
// rather than guessing.
//
// What lives here is the shared test that decides whether a path is a question
// at all (boundaryQuestion), the action it maps to, and the two helpers —
// WriteAllowed, which keeps the asker and the writer from disagreeing, and
// blockRefused, which refuses a path the user already said no about — the
// consent gate drives.
//
// Credential material is not on the table at all: a path the static floor
// refuses is passed through untouched, so the floor denies it and no dialog
// invites the user to hand over ~/.ssh or ~/.zshrc by pressing a key.

import (
	"fmt"
	"strings"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/grants"
	"github.com/getan/golder/internal/permissions"
)

// insideSystemRoots reports whether p lives in the always-readable system
// whitelist. Asking about /usr/bin in every command that runs a program would
// train people to approve without reading, and the sandbox admits these paths
// anyway, so a path there is not a boundary question.
func insideSystemRoots(p string) bool {
	// The comparison lives in permissions so this gate and the sandbox cannot
	// disagree about "always readable": it checks both the literal spelling
	// (what the sandbox's own rules use) and the symlink-resolved one (where
	// "/tmp/x" and "/private/tmp/x" are one place).
	return permissions.InsideSystemReadRoots(p)
}

// boundaryQuestion reports whether one path needs the user's answer, given the
// path as written and as resolved. It is the whole test, in one place, because
// two callers need the same answer and must not drift:
//
//   - the consent gate, which asks the question;
//   - the reviewer's state (AuthFacts), which tells the model whether a consent
//     question is in front of this call — a model told "outside the workspace"
//     without "and already granted" either re-asks what the user just approved
//     or approves what nobody has seen.
//
// Each clause is a reason the question is not worth asking:
//
//   - inside the workspace, or inside a root the session treats as its own (the
//     skills directory golder advertises itself);
//   - credential material, which is the static floor's to refuse: passing it
//     through untouched means the floor denies the call and no dialog offers to
//     grant what the user should never hand over by pressing a key;
//   - already granted, through either reader — the legacy registry (the
//     read-root list for a read, the sandbox's writable roots for a write) or
//     the authorization table (what every dialog answer records now);
//   - the system whitelist, which the sandbox admits anyway, so asking would
//     only train people to approve without reading. It applies to READS only:
//     those paths are read-only by design, so a write there is a question, and
//     a prompt that offers to grant /usr is a prompt that should not exist.
//
// The two sides do not share their silencers, and the asymmetry is the point:
// a read at /etc/hosts needs no answer, a write to it is a question the user
// should see; a directory granted for reading says nothing about writing it,
// and a sandbox writable root (which implies reading) says nothing about
// whether a read has been approved.
//
// A REFUSAL outranks all of it. The table's answer for this path can be "the
// user said no", and that is not a reason to stay silent — it is a reason to
// block without asking, which is why the result is three-valued rather than a
// boolean.
func boundaryQuestion(raw, resolved, absRoot string, extraRoots []string, op grants.Capability, writableAt func(string) bool) boundaryAction {
	if permissions.Inside(absRoot, resolved) || insideAnyRoot(extraRoots, resolved) {
		return boundarySilent
	}
	if op.Has(grants.Write) {
		// Writing into live secret material is the static floor's to refuse, so
		// the path passes through untouched and no dialog offers it a key.
		if _, bad := secretPath(raw); bad {
			return boundarySilent
		}
		if grants.Check(resolved, grants.Write).Decision == grants.Denied {
			return boundaryRefused
		}
		if WriteAllowed(resolved, writableAt) {
			return boundarySilent
		}
		if permissions.InsideSystemWriteRoots(resolved) {
			return boundarySilent
		}
		return boundaryAsk
	}
	if _, bad := credentialPathRead(raw); bad {
		return boundarySilent
	}
	if grants.Check(resolved, grants.Read).Decision == grants.Denied {
		return boundaryRefused
	}
	if grants.Allows(resolved, grants.Read) {
		return boundarySilent
	}
	if insideSystemRoots(resolved) {
		return boundarySilent
	}
	return boundaryAsk
}

// boundaryAction is what the gate owes one path.
type boundaryAction uint8

const (
	// boundarySilent: nothing at all. The path is inside the boundary, already
	// granted, credential material the static floor owns, or on the system
	// whitelist the sandbox admits anyway.
	boundarySilent boundaryAction = iota
	// boundaryAsk: put the question to the user.
	boundaryAsk
	// boundaryRefused: the user already answered no about this path. The call is
	// blocked without asking, because a question already answered is not worth
	// putting twice.
	boundaryRefused
)

// WriteAllowed reports whether a write to path is already authorized for this
// session: the session's writable registry covers it, or the authorization
// table carries a write grant for it.
//
// It is shared, deliberately, by the two callers that must not disagree — the
// gate, which decides whether to ASK, and the write tool, which decides whether
// to WRITE. The gate stays silent exactly when this answers true, so a mismatch
// is a failure the user sees: a patch refused after they approved it, or a
// question about a directory they just granted. Both sources are consulted
// because both are answers the user gave: the registry is where config.toml,
// permissions.toml and the sandbox dialog put theirs, the table is where every
// dialog answer now lands — including the one-call "allow this write" row,
// which is recorded as an exact-path grant and would otherwise silence the
// question while the tool kept refusing.
//
// writableAt may be nil for a caller that has no registry to consult; the table
// alone then decides, which is the stricter reading.
func WriteAllowed(path string, writableAt func(string) bool) bool {
	if writableAt != nil && writableAt(path) {
		return true
	}
	return grants.Allows(path, grants.Write)
}

// insideAnyRoot reports whether target is inside one of the extra trusted roots.
func insideAnyRoot(roots []string, target string) bool {
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		if permissions.InsideResolved(root, target) {
			return true
		}
	}
	return false
}

// blockRefused refuses a path the user already answered no about, without
// asking again. The message says so plainly and names how to change the answer:
// a refusal that reads like a fresh denial would leave the model (and the
// person) thinking the question had just been put, and a block nobody can lift
// is a trap rather than a decision.
func blockRefused(call agentcore.AgentToolCall, target string, writing bool) *agentcore.BeforeToolCallDecision {
	verb, cmd := "read", "/boundary readable add"
	if writing {
		verb, cmd = "write", "/boundary writable add"
	}
	msg := fmt.Sprintf("tool %q blocked: the %s of %s was refused earlier in this session, so it is not asked again", call.Name, verb, target)
	msg += fmt.Sprintf("; to change that, run %s %s, or start a new session", cmd, target)
	return &agentcore.BeforeToolCallDecision{
		Block:   true,
		Content: &agentcore.ContentList{agentcore.NewTextContent(msg)},
	}
}
