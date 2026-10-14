package accessgate

// Tests for the unified access gate. The load-bearing cases:
//
//   - a device node is not the filesystem root (the reported bug);
//   - every path a call names is reported, and resolution is skipped for the
//     ones the system whitelist already covers;
//   - a multi-path command asks ONCE;
//   - an unparsable command is unknown, not path-free;
//   - a refusal in the table is honoured rather than re-asked.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/grants"
)

// workspace returns a real workspace root and a synthetic path outside it.
//
// The outside path is deliberately NOT under t.TempDir(): the system read
// whitelist includes the process temp directory (os.TempDir()), because the
// sandbox admits it, so a temp path would be correctly treated as "not a
// boundary question" and every assertion below would be untestable. The path is
// fictional — the gate resolves lexically and never stats — so no test touches
// anything on disk outside its own temp tree.
const outsideRoot = "/golder-accessgate-outside"

func workspace(t *testing.T) (root, outside string) {
	t.Helper()
	root = filepath.Join(t.TempDir(), "ws")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return root, outsideRoot
}

// newStore builds a grants table in a temp dir.
func newStore(t *testing.T) *grants.Store {
	t.Helper()
	s, err := grants.New(grants.Options{Path: filepath.Join(t.TempDir(), "grants.json")})
	if err != nil {
		t.Fatalf("grants.New: %v", err)
	}
	return s
}

func bashArgs(cmd string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"command": cmd})
	return b
}

// readOnly is the recognizer production injects (judge.ReadOnlyShellCommand).
// A test that expects a read-only operation must supply one, because a gate
// with no recognizer treats every argument as possibly writing — which is the
// safe default, and the reason these tests would otherwise ask for write access
// a plain `cat` never needs.
func readOnly(cmd string) bool {
	for _, prefix := range []string{"cat ", "ls ", "echo "} {
		if strings.HasPrefix(cmd, prefix) {
			return true
		}
	}
	return false
}

func TestDeviceNodeIsNotTheFilesystemRoot(t *testing.T) {
	// The reported bug: `ls /dev/null` was offered as a question about "/".
	// The path comes from a parse now, and /dev is in the system whitelist, so
	// the call is silent.
	root, _ := workspace(t)
	res := Check("bash", bashArgs("ls /dev/null"), Options{WorkspaceRoot: root, Store: newStore(t)})
	if res.Ask {
		t.Errorf("ls /dev/null asked a question: pending=%+v unknown=%v", res.Pending, res.Unknown)
	}
	// The confirmation that the silence above is about /dev and not about the
	// gate ignoring paths: the same shape outside every whitelist does ask.
	if res := Check("bash", bashArgs("ls "+outsideRoot+"/x"), Options{WorkspaceRoot: root, Store: newStore(t)}); !res.Ask {
		t.Error("a path outside the workspace and the whitelist asked nothing")
	}
}

func TestInsideWorkspaceIsNotAQuestion(t *testing.T) {
	root, _ := workspace(t)
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	for _, cmd := range []string{
		"cat notes.md",
		"ls -la .",
		"grep -rn x internal/",
	} {
		res := Check("bash", bashArgs(cmd), Options{WorkspaceRoot: root, Store: newStore(t)})
		if res.Ask {
			t.Errorf("%q asked a question about its own workspace: %+v", cmd, res.Pending)
		}
	}
}

func TestOutsidePathAsksOnceWithEveryPath(t *testing.T) {
	root, outside := workspace(t)
	a := filepath.Join(outside, "a.txt")
	b := filepath.Join(outside, "b.txt")
	cmd := "cat " + a + " " + b
	res := Check("bash", bashArgs(cmd), Options{WorkspaceRoot: root, Store: newStore(t)})
	if !res.Ask {
		t.Fatal("a command reading two outside files asked nothing")
	}
	// One question covering both paths, not two questions.
	if len(res.Pending) != 2 {
		t.Fatalf("pending = %+v, want both paths in one question", res.Pending)
	}
	got := []string{res.Pending[0].Path, res.Pending[1].Path}
	for _, want := range []string{a, b} {
		found := false
		for _, g := range got {
			if g == want {
				found = true
			}
		}
		if !found {
			t.Errorf("path %s missing from the question: %v", want, got)
		}
	}
	// The order is stable, so the same call asks the same question.
	if got[0] > got[1] {
		t.Errorf("paths are not in a stable order: %v", got)
	}
}

func TestGrantedPathIsSilent(t *testing.T) {
	root, outside := workspace(t)
	store := newStore(t)
	if _, err := store.Add(grants.Grant{Path: outside, Allow: grants.Read}); err != nil {
		t.Fatalf("grants.Add: %v", err)
	}
	res := Check("bash", bashArgs("cat "+filepath.Join(outside, "a.txt")),
		Options{WorkspaceRoot: root, Store: store, ReadOnly: readOnly})
	if res.Ask {
		t.Errorf("a granted path asked a question: %+v", res.Pending)
	}
}

func TestRefusalIsReportedRatherThanReAsked(t *testing.T) {
	root, outside := workspace(t)
	store := newStore(t)
	if _, err := store.Refuse(outside, grants.Read, grants.Remembered); err != nil {
		t.Fatalf("grants.Refuse: %v", err)
	}
	res := Check("bash", bashArgs("cat "+filepath.Join(outside, "a.txt")),
		Options{WorkspaceRoot: root, Store: store, ReadOnly: readOnly})
	if len(res.Denied) == 0 {
		t.Fatalf("a refused path was not reported: %+v", res)
	}
	// A refusal is an answer, so the gate must not put the same question again.
	if len(res.Pending) != 0 {
		t.Errorf("pending = %+v, want none after a refusal", res.Pending)
	}
}

// TestReadOnlyNarrowingNeedsTheRecognizer pins WHY the recognizer is injected
// rather than assumed: without it a plain read asks for write access, which is
// how a prompt becomes something people approve without reading.
func TestReadOnlyNarrowingNeedsTheRecognizer(t *testing.T) {
	root, outside := workspace(t)
	cmd := "cat " + filepath.Join(outside, "a.txt")

	bare := Check("bash", bashArgs(cmd), Options{WorkspaceRoot: root, ReadOnly: nil})
	if len(bare.Pending) != 1 || !bare.Pending[0].Op.Has(grants.Write) {
		t.Errorf("without a recognizer the op = %+v, want read+write (the safe default)", bare.Pending)
	}

	recognized := Check("bash", bashArgs(cmd), Options{WorkspaceRoot: root, ReadOnly: readOnly})
	if len(recognized.Pending) != 1 || recognized.Pending[0].Op != grants.Read {
		t.Errorf("with a recognizer the op = %+v, want read", recognized.Pending)
	}
}

func TestUnparsableCommandIsUnknownNotPathFree(t *testing.T) {
	// The failure this prevents: a command the parser cannot read reporting no
	// paths, which a caller would read as "nothing to ask about".
	root, _ := workspace(t)
	res := Check("bash", bashArgs(`ls "unterminated`), Options{WorkspaceRoot: root, Store: newStore(t)})
	if !res.Unknown {
		t.Errorf("an unparsable command was not marked unknown: %+v", res)
	}
	if !res.Ask {
		t.Error("an unparsable command asked nothing")
	}
}

func TestNativeToolsUseTheSameBoundary(t *testing.T) {
	// bash and the file tools must agree: the same outside path produces the
	// same question whatever tool names it.
	root, outside := workspace(t)
	target := filepath.Join(outside, "a.txt")

	for _, c := range []struct {
		tool string
		args json.RawMessage
	}{
		{"read", json.RawMessage(`{"path":"` + target + `"}`)},
		{"view_image", json.RawMessage(`{"path":"` + target + `"}`)},
		{"grep", json.RawMessage(`{"path":"` + target + `"}`)},
		{"bash", bashArgs("cat " + target)},
	} {
		res := Check(c.tool, c.args, Options{WorkspaceRoot: root, Store: newStore(t)})
		if !res.Ask {
			t.Errorf("%s on an outside path asked nothing", c.tool)
			continue
		}
		found := false
		for _, p := range res.Pending {
			if p.Abs == target {
				found = true
			}
		}
		if !found {
			t.Errorf("%s pending = %+v, want %s", c.tool, res.Pending, target)
		}
	}
}

func TestListPathArgumentsAreCovered(t *testing.T) {
	root, outside := workspace(t)
	args := json.RawMessage(`{"path":["` + outside + `","` + root + `"]}`)
	res := Check("grep", args, Options{WorkspaceRoot: root, Store: newStore(t)})
	if !res.Ask {
		t.Fatal("a list-form path argument asked nothing")
	}
	// Only the outside entry is pending: the workspace entry is not a question.
	if len(res.Pending) != 1 {
		t.Fatalf("pending = %+v, want only the outside path", res.Pending)
	}
	if !strings.HasSuffix(res.Pending[0].Abs, "outside") {
		t.Errorf("pending path = %q, want the outside one", res.Pending[0].Abs)
	}
}

func TestApplyPatchOutsideAsks(t *testing.T) {
	root, outside := workspace(t)
	patchText := "*** Begin Patch\n*** Add File: " + filepath.Join(outside, "new.txt") + "\n+hi\n*** End Patch\n"
	args, _ := json.Marshal(map[string]string{"patch": patchText})
	res := Check("apply_patch", args, Options{WorkspaceRoot: root, Store: newStore(t)})
	if !res.Ask {
		t.Fatalf("a patch writing outside asked nothing: %+v", res)
	}
	// A patch writes, so the question is about write access.
	if !res.Pending[0].Op.Has(grants.Write) {
		t.Errorf("op = %v, want write for a patch", res.Pending[0].Op)
	}
}

func TestRedirectTargetIsVisible(t *testing.T) {
	// A redirect names a path the argv never mentions; a gate reading only
	// arguments would miss it entirely.
	root, outside := workspace(t)
	target := filepath.Join(outside, "out.txt")
	res := Check("bash", bashArgs("echo hi > "+target), Options{WorkspaceRoot: root, Store: newStore(t)})
	if !res.Ask {
		t.Fatal("a redirect outside the workspace asked nothing")
	}
	if !res.Pending[0].Op.Has(grants.Write) {
		t.Errorf("op = %v, want write for a redirect", res.Pending[0].Op)
	}
}

func TestNestedCommandPathIsReported(t *testing.T) {
	root, outside := workspace(t)
	target := filepath.Join(outside, "hidden.txt")
	res := Check("bash", bashArgs(`echo "$(cat `+target+`)"`), Options{WorkspaceRoot: root, Store: newStore(t)})
	if !res.Ask {
		t.Fatal("a path inside a command substitution asked nothing")
	}
	var nested bool
	for _, p := range res.Pending {
		if p.Nested && p.Abs == target {
			nested = true
		}
	}
	if !nested {
		t.Errorf("pending = %+v, want the nested path marked as such", res.Pending)
	}
}

func TestUnknownToolIsNotGated(t *testing.T) {
	root, _ := workspace(t)
	// A tool with no declared paths (webfetch, websearch, todo) is not a path
	// question; its reach is the network or memory, which is the reviewer's.
	res := Check("webfetch", json.RawMessage(`{"url":"https://example.com"}`), Options{WorkspaceRoot: root})
	if res.Ask {
		t.Errorf("an ungated tool asked a path question: %+v", res)
	}
}

func TestOfferNeverWidensBeyondWhatWasNamed(t *testing.T) {
	// The over-grant this design exists to avoid: answering one question must
	// not hand over a tree nobody named.
	root, outside := workspace(t)
	store := newStore(t)
	deep := filepath.Join(outside, "a", "b")
	res := Check("bash", bashArgs("ls "+deep), Options{WorkspaceRoot: root, Store: store})
	if !res.Ask {
		t.Fatal("nothing to ask about")
	}
	offers := Offer(res.Pending, Options{WorkspaceRoot: root, Store: store})
	if len(offers) != 1 {
		t.Fatalf("offers = %+v, want one", offers)
	}
	if offers[0].Path != deep {
		t.Errorf("offer = %q, want the named directory %q", offers[0].Path, deep)
	}
	// It must not climb to the common parent: /tmp/.../outside is nobody's
	// business here.
	if offers[0].Path == outside {
		t.Error("the offer widened to the parent directory")
	}
}

func TestOfferRefusesToPromiseARootTheStoreWouldReject(t *testing.T) {
	root, _ := workspace(t)
	res := Check("bash", bashArgs("ls /"), Options{WorkspaceRoot: root, Store: newStore(t)})
	offers := Offer(res.Pending, Options{WorkspaceRoot: root})
	// "/" cannot be granted, so the dialog must not offer it as a rememberable
	// directory; promising something the grant call then refuses is worse than
	// offering nothing.
	for _, o := range offers {
		if o.Path == "/" || o.Path == string(filepath.Separator) {
			t.Errorf("offered the filesystem root: %+v", offers)
		}
	}
}

func TestExactResourceOffersItsDirectoryAsASubtree(t *testing.T) {
	// A tool naming one file offers the directory holding it — because that is
	// the only useful rememberable grant — but the read tool's own path
	// resource stays Exact so a session grant cannot be widened by accident.
	root, outside := workspace(t)
	target := filepath.Join(outside, "one.txt")
	res := Check("read", json.RawMessage(`{"path":"`+target+`"}`), Options{WorkspaceRoot: root, Store: newStore(t)})
	if len(res.Pending) != 1 || !res.Pending[0].Exact {
		t.Fatalf("pending = %+v, want one exact resource", res.Pending)
	}
	offers := Offer(res.Pending, Options{WorkspaceRoot: root})
	if len(offers) != 1 {
		t.Fatalf("offers = %+v, want one", offers)
	}
	if offers[0].Path != outside {
		t.Errorf("offer = %q, want the containing directory %q", offers[0].Path, outside)
	}
	if offers[0].Subtree {
		t.Error("the offer claimed a subtree for a single-file resource")
	}
}

func TestEmptyAndMissingArgumentsAreSafe(t *testing.T) {
	// A malformed argument must not panic, and must not silently read as
	// "nothing to ask about" for a tool whose paths are its arguments.
	root, _ := workspace(t)
	for _, c := range []struct {
		tool string
		args string
	}{
		{"read", ``},
		{"read", `{}`},
		{"read", `{"path":""}`},
		{"read", `not json`},
		{"grep", `{"path":[]}`},
		{"bash", `{}`},
		{"bash", `{"command":""}`},
	} {
		res := Check(c.tool, json.RawMessage(c.args), Options{WorkspaceRoot: root, Store: newStore(t)})
		// The only requirement: no panic, and no fabricated allowance.
		if res.Unknown && !res.Ask {
			t.Errorf("%s %s: unknown without asking", c.tool, c.args)
		}
	}
}

func TestToolsCoverThePathBearingTools(t *testing.T) {
	got := strings.Join(Tools(), ",")
	want := "apply_patch,bash,find,grep,ls,read,view_image"
	if got != want {
		t.Errorf("gated tools = %q, want %q", got, want)
	}
}

func TestSystemWhitelistAnswersForTheWholePathTree(t *testing.T) {
	root, _ := workspace(t)
	// /usr/bin, /etc, /dev, /proc are all admitted by the sandbox, so asking
	// about them would train the user to approve without reading.
	for _, cmd := range []string{"cat /etc/hosts", "ls /usr/bin", "cat /dev/null", "ls /proc/self"} {
		res := Check("bash", bashArgs(cmd), Options{WorkspaceRoot: root, Store: newStore(t)})
		if res.Ask {
			t.Errorf("%q asked a question about a system path: %+v", cmd, res.Pending)
		}
	}
}
