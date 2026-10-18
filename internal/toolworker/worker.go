// Package toolworker runs file tools in a fresh, per-call OS sandbox. The
// process running the agent is never sandboxed: credentials, model connections,
// history and the rewind journal stay in the parent. Only approved tool input,
// the workspace and the grant snapshot cross the process boundary.
package toolworker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/grants"
	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/patch"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/seatbelt"
)

const Flag = "--tool-worker"

// Request carries one approved call. No credential or persistent state path is
// transferred; the worker never opens grants.json. Exact grants are copied from
// the call context after the boundary gate has answered.
type Request struct {
	Name       string          `json:"name"`
	ID         string          `json:"id"`
	Args       json.RawMessage `json:"args"`
	Root       string          `json:"root"`
	ExtraRoots []string        `json:"extraRoots,omitempty"`
	Grants     grants.Entries  `json:"-"`
	Rows       []grantWire     `json:"grants"`
	ReadFile   string          `json:"readFile,omitempty"`
	WriteFile  string          `json:"writeFile,omitempty"`
}

// grantWire is only for this one-call IPC, not the on-disk grants schema.
// Grant.Allow/Deny are tagged json:"-" intentionally (disk rows use letter
// strings); sending Entries directly would silently drop BOTH capability bits
// and turn every worker grant into an undecided row.
type grantWire struct {
	Path     string            `json:"path"`
	Allow    grants.Capability `json:"allow"`
	Deny     grants.Capability `json:"deny"`
	Scope    grants.Scope      `json:"scope"`
	Lifetime grants.Lifetime   `json:"lifetime"`
	Source   string            `json:"source,omitempty"`
}

func grantRows(entries grants.Entries) []grantWire {
	var out []grantWire
	for _, g := range entries.All() {
		out = append(out, grantWire{g.Path, g.Allow, g.Deny, g.Scope, g.Lifetime, g.Source})
	}
	return out
}

func restoreGrantRows(rows []grantWire) grants.Entries {
	var out grants.Entries
	for _, r := range rows {
		g := grants.Grant{Path: r.Path, Allow: r.Allow, Deny: r.Deny, Scope: r.Scope, Lifetime: r.Lifetime, Source: r.Source}
		if r.Lifetime == grants.Remembered {
			out.Remembered = append(out.Remembered, g)
		} else {
			out.Session = append(out.Session, g)
		}
	}
	return out
}

type frame struct {
	Kind      string                       `json:"kind"`
	Result    agentcore.AgentToolResult    `json:"result,omitempty"`
	Snapshots []agenttool.SnapshotTransfer `json:"snapshots,omitempty"`
	Error     string                       `json:"error,omitempty"`
}

// Tool decorates a built-in file tool without changing its public schema,
// scheduling mode or identity. Only builtin file tools are decorated; shell,
// network, session and in-memory tools use their own execution routes.
type Tool struct {
	Original   agentcore.AgentTool
	Root       string
	ExtraRoots []string
	Snap       *agenttool.FileSnapshotRecorder
	Runner     seatbelt.CallRunner
	Mode       *permissions.State
	// RequireIsolation, when set, refuses a call whose OS runner is missing
	// instead of running the native tool in-process. Production sets it where
	// isolation is the default; on a platform with no sandbox implementation it
	// stays false so the tools keep working as they always did.
	RequireIsolation bool
	// Binary overrides the current executable (integration tests only).
	Binary string
}

func (t *Tool) Name() string                               { return t.Original.Name() }
func (t *Tool) Description() string                        { return t.Original.Description() }
func (t *Tool) Schema() json.RawMessage                    { return t.Original.Schema() }
func (t *Tool) ExecutionMode() agentcore.ToolExecutionMode { return t.Original.ExecutionMode() }

func (t *Tool) Execute(ctx context.Context, id string, args json.RawMessage, _ agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	if t.Mode != nil && t.Mode.Mode() == permissions.FullAccess {
		return t.Original.Execute(ctx, id, args, nil)
	}
	if agentcore.DirectGrantedFromContext(ctx) && !agentcore.SandboxRequestedFromContext(ctx) {
		// A gate (not the tool arguments) explicitly approved running this
		// one call without the OS runner. The native tool's own path policy and
		// one-call read/write grants still apply.
		return t.Original.Execute(ctx, id, args, nil)
	}
	if t.Name() == "apply_patch" && t.Snap == nil {
		return failed("no rewind recorder for isolated patch; refusing to write")
	}
	if t.Runner == nil {
		if t.RequireIsolation {
			return failed("no OS sandbox for this file tool; refusing to run outside the sandbox")
		}
		return t.Original.Execute(ctx, id, args, nil)
	}
	bin := t.Binary
	if bin == "" {
		var err error
		bin, err = os.Executable()
		if err != nil {
			return failed("cannot find tool worker executable: " + err.Error())
		}
	}
	req := Request{Name: t.Name(), ID: id, Args: args, Root: t.Root, ExtraRoots: t.ExtraRoots,
		ReadFile: agentcore.ReadGrantFromContext(ctx), WriteFile: agentcore.WriteGrantFromContext(ctx)}
	if s := grants.Process(); s != nil {
		req.Grants = s.Entries()
		req.Rows = grantRows(req.Grants)
	}
	policy, perr := workerPolicy(req, t.ExtraRoots, bin)
	if perr != nil {
		return failed(perr.Error())
	}
	if t.Name() == "apply_patch" {
		// Patches may edit advertised skills, while reads cannot write there.
		policy.WriteDirs = append(policy.WriteDirs, safePaths(t.ExtraRoots)...)
	} else {
		// The read tools cannot write. Do not give an in-process bug or a
		// compromised rg subprocess write access merely because the user granted
		// that directory for a DIFFERENT tool.
		policy.WriteDirs, policy.WriteFiles = nil, nil
		policy.ReadOnlyFilesystem = true
	}
	seatbelt.RefusedPaths(&policy)
	argv, cleanup, err := t.Runner.SandboxCall([]string{bin, Flag}, t.Root, policy)
	if err != nil {
		return failed("cannot create tool sandbox: " + err.Error())
	}
	if cleanup != nil {
		defer cleanup()
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = t.Root
	cmd.Env = workerEnv()
	cmd.WaitDelay = 2 * time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return failed("tool worker stdin: " + err.Error())
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return failed("tool worker stdout: " + err.Error())
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		return failed("cannot start tool worker: " + err.Error())
	}
	enc := json.NewEncoder(stdin)
	dec := json.NewDecoder(bufio.NewReader(stdout))
	if err = enc.Encode(req); err != nil {
		_ = stdin.Close()
		_ = cmd.Wait()
		return failed("cannot send tool call: " + err.Error())
	}
	var outcome agentcore.AgentToolResult
	var haveResult bool
	for {
		var f frame
		if err = dec.Decode(&f); err != nil {
			break
		}
		switch f.Kind {
		case "baseline":
			// ACK *before* the worker writes anything. If it dies during the
			// write, /rewind still knows the previous content.
			t.Snap.ImportWorkerBaseline(f.Snapshots)
			if err = enc.Encode(frame{Kind: "ack"}); err != nil {
				break
			}
		case "result":
			outcome, haveResult = f.Result, true
		case "error":
			err = errors.New(f.Error)
		}
		if err != nil || haveResult {
			break
		}
	}
	_ = stdin.Close()
	waitErr := cmd.Wait()
	if haveResult && waitErr == nil {
		return outcome, nil
	}
	if err == nil || errors.Is(err, io.EOF) {
		err = waitErr
	}
	if err == nil {
		err = errors.New("worker returned no result")
	}
	return failed("tool worker failed: " + err.Error() + workerStderr(stderr.String()))
}

// workerPolicy builds the mount set for one worker call. Ambient grants (the
// session table) and configured extra roots are filtered: a grant for a
// protected path must not become a worker mount, and — the bug this fixes — it
// must not fail an unrelated call either. One session read grant for ~/.ssh
// used to make every later tool call fail with "grant targets protected state
// or credential material", because the check rejected the whole request
// instead of dropping the one path.
//
// The one-call read/write grants are different: the caller asked for that exact
// path on this call, so a protected target is a real refusal, not ambient noise.
func workerPolicy(req Request, extraRoots []string, bin string) (seatbelt.CallPolicy, error) {
	policy := seatbelt.CallPolicy{ReadDirs: safePaths(extraRoots), ReadFiles: []string{bin}}
	for _, g := range req.Grants.All() {
		if g.Allow == 0 {
			continue
		}
		if !safeWorkerPath(g.Path) {
			continue
		}
		// The worker gets only effective grants, including exact "this read"
		// grants recorded in an earlier call. The whole table travels as well,
		// so a later exact refusal still overrides an ancestor at the tool level.
		if g.Allow.Has(grants.Read) && grants.Allows(g.Path, grants.Read) {
			if g.Scope == grants.Subtree {
				policy.ReadDirs = append(policy.ReadDirs, g.Path)
			} else {
				policy.ReadFiles = append(policy.ReadFiles, g.Path)
			}
		}
		if g.Allow.Has(grants.Write) && grants.Allows(g.Path, grants.Write) {
			if g.Scope == grants.Subtree {
				policy.WriteDirs = append(policy.WriteDirs, g.Path)
			} else {
				policy.WriteFiles = append(policy.WriteFiles, g.Path)
			}
		}
	}
	if req.ReadFile != "" {
		if !safeWorkerPath(req.ReadFile) {
			return seatbelt.CallPolicy{}, fmt.Errorf("tool worker grant targets protected state or credential material")
		}
		policy.ReadFiles = append(policy.ReadFiles, req.ReadFile)
	}
	if req.WriteFile != "" {
		if !safeWorkerPath(req.WriteFile) {
			return seatbelt.CallPolicy{}, fmt.Errorf("tool worker grant targets protected state or credential material")
		}
		policy.WriteFiles = append(policy.WriteFiles, req.WriteFile)
	}
	return policy, nil
}

// safeWorkerPath reports whether p may be mounted into the worker. Protected
// state and credential material may never cross that boundary.
func safeWorkerPath(p string) bool {
	_, bad := judge.SecretPathReason(p)
	return !bad
}

// safePaths drops every path a worker must not mount.
func safePaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if safeWorkerPath(p) {
			out = append(out, p)
		}
	}
	return out
}

func failed(why string) (agentcore.AgentToolResult, error) {
	return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent(why)}}, errors.New(why)
}

func workerStderr(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 500 {
		s = s[:500] + "…"
	}
	if s == "" {
		return ""
	}
	return " (" + s + ")"
}

// The model-facing tool receives the user's environment; the file worker does
// not. In particular API keys and proxy credentials must not cross into a
// process that can execute an untrusted binary (rg) under a shared UID.
func workerEnv() []string {
	var out []string
	for _, key := range []string{"PATH", "HOME", "TMPDIR", "TMP", "TEMP", "LANG", "LC_ALL", "SYSTEMROOT", "WINDIR"} {
		if v, ok := os.LookupEnv(key); ok {
			out = append(out, key+"="+v)
		}
	}
	// The sandbox needs the state root so it can reassert its deny rule;
	// it does not make the directory readable in the worker.
	if h := os.Getenv("GOLDER_HOME"); h != "" {
		out = append(out, "GOLDER_HOME="+h)
	}
	return out
}

// Serve runs in the worker subprocess, exclusively. It consumes one request,
// creates a grant table in memory, executes a known file tool, then exits. The
// parent already passed the call through its three gates; these checks are a
// second layer, not a substitute for the parent gate or the OS sandbox.
func Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	dec := json.NewDecoder(in)
	enc := json.NewEncoder(out)
	var req Request
	if err := dec.Decode(&req); err != nil {
		return fmt.Errorf("tool worker request: %w", err)
	}
	if !filepath.IsAbs(req.Root) {
		return fmt.Errorf("tool worker: workspace root must be absolute")
	}
	s, err := grants.FromSnapshot(restoreGrantRows(req.Rows))
	if err != nil {
		return err
	}
	grants.SetProcess(s)
	defer grants.SetProcess(nil)
	if req.ReadFile != "" {
		ctx = agentcore.WithReadGrant(ctx, req.ReadFile)
	}
	if req.WriteFile != "" {
		ctx = agentcore.WithWriteGrant(ctx, req.WriteFile)
	}
	var tool agentcore.AgentTool
	switch req.Name {
	case "read":
		tool = &agenttool.ReadTool{Root: req.Root, ExtraRoots: req.ExtraRoots}
	case "view_image":
		tool = &agenttool.ViewImageTool{Root: req.Root, ExtraRoots: req.ExtraRoots}
	case "grep":
		tool = &agenttool.GrepTool{Root: req.Root}
	case "find":
		tool = &agenttool.FindTool{Root: req.Root}
	case "ls":
		tool = &agenttool.LsTool{Root: req.Root}
	case "apply_patch":
		snap := agenttool.NewFileSnapshotRecorder()
		p := &agenttool.ApplyPatchTool{Root: req.Root, ExtraRoots: req.ExtraRoots, Snap: snap,
			WritableAt: func(path string) bool { return grants.Allows(path, grants.Write) }}
		p.BeforePersist = func(changes []patch.Change) error {
			for _, ch := range changes {
				if ch.OldPath != "" {
					snap.Record(ch.OldPath)
				}
				snap.Record(ch.Path)
			}
			if err := enc.Encode(frame{Kind: "baseline", Snapshots: snap.PendingForWorker()}); err != nil {
				return err
			}
			var ack frame
			if err := dec.Decode(&ack); err != nil {
				return err
			}
			if ack.Kind != "ack" {
				return errors.New("parent did not acknowledge rewind baseline")
			}
			return nil
		}
		tool = p
	default:
		return fmt.Errorf("tool worker: unsupported tool %q", req.Name)
	}
	res, err := tool.Execute(ctx, req.ID, req.Args, nil)
	if err != nil {
		return enc.Encode(frame{Kind: "error", Error: err.Error()})
	}
	return enc.Encode(frame{Kind: "result", Result: res})
}
