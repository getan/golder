package agenttool

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/seatbelt"
	"github.com/getan/golder/internal/tuiosctl"
)

// ControlGuard is session-local authority, shared only by the three broker
// tools in one registry. Repository metadata and pane titles cannot grant it.
type ControlGuard struct {
	Root     string
	Mode     *permissions.State
	Runner   seatbelt.CallRunner
	mu       sync.Mutex
	launchMu sync.Mutex
	panes    map[string]controlledPane
	browsers map[string]controlledBrowser
	id       string
}

type controlledPane struct {
	cwd            string
	network        bool
	isolated       bool
	browser        bool
	browserRuntime bool
	sealed         bool
	policy         [32]byte
	cleanup        func()
}

type controlledBrowser struct {
	socket         string
	window         string
	isolated       bool
	browserRuntime bool
	policy         [32]byte
}

func NewControlGuard(root string) *ControlGuard {
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		panic("control identity: " + err.Error())
	}
	return &ControlGuard{Root: root, Mode: permissions.New(permissions.Auto), Runner: seatbelt.New(root),
		panes: map[string]controlledPane{}, browsers: map[string]controlledBrowser{}, id: hex.EncodeToString(id[:])}
}

func (g *ControlGuard) full() bool {
	return g != nil && g.Mode != nil && g.Mode.Mode() == permissions.FullAccess
}

func (g *ControlGuard) network(ctx context.Context) bool {
	return g.full() || agentcore.NetworkGrantedFromContext(ctx)
}

func (g *ControlGuard) host(ctx context.Context, requested bool) bool {
	if agentcore.SandboxRequestedFromContext(ctx) || seatbelt.ModeFromEnv() == seatbelt.ModeEnforce {
		return false
	}
	return g.full() || (requested && agentcore.HostControlGrantedFromContext(ctx))
}

func (g *ControlGuard) pane(id string) (controlledPane, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	p, ok := g.panes[id]
	return p, ok
}

func (g *ControlGuard) checkPane(ctx context.Context, win tuiosctl.Window, own string, host, input bool) error {
	if win.WindowID == own && input {
		return errors.New("refusing agent input into its own pane or approval UI")
	}
	if input && (win.AgentState == "needs_input" || win.AgentState == "needs-input" || strings.Contains(strings.ToLower(win.ForegroundCmd), "golder")) {
		return errors.New("refusing input into an agent/approval pane; only the person may answer approvals")
	}
	p, managed := g.pane(win.WindowID)
	if !managed || (!p.isolated && !g.full()) {
		if !g.host(ctx, host) {
			return errors.New("pane is not owned by this agent; host_control:true requires a separate one-call authorization")
		}
		if input && !g.network(ctx) {
			return errors.New("external pane input requires reviewed network/host authority")
		}
		return nil
	}
	if input && p.browser {
		return errors.New("use the browser tool for browser panes; terminal input could bypass browser action review")
	}
	if input && (p.sealed || p.policy != controlPolicyStamp()) {
		return errors.New("pane permissions changed or its one-call grant has ended; create a new controlled pane")
	}
	if input && p.network && !g.network(ctx) {
		return errors.New("this pane has network access; request network:on for this input call")
	}
	if agentcore.SandboxRequestedFromContext(ctx) && !p.isolated {
		return errors.New("the target pane cannot satisfy the required sandbox")
	}
	return nil
}

func (g *ControlGuard) checkBrowser(ctx context.Context, id, socket string, host bool) error {
	g.mu.Lock()
	want, ok := g.browsers[id]
	g.mu.Unlock()
	if ok && want.socket == canonicalBrowserSocket(socket) && (want.isolated || ((want.browserRuntime || g.full()) && !agentcore.SandboxRequestedFromContext(ctx) && seatbelt.ModeFromEnv() != seatbelt.ModeEnforce)) {
		if want.policy != controlPolicyStamp() {
			return errors.New("browser sandbox policy changed; open a new controlled browser")
		}
		return nil
	}
	if !g.host(ctx, host) {
		return errors.New("browser is not owned by this agent; host_control:true requires a separate one-call authorization")
	}
	return nil
}

func (g *ControlGuard) rememberBrowser(id, socket, window string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	p := g.panes[window]
	g.browsers[id] = controlledBrowser{socket: canonicalBrowserSocket(socket), window: window, isolated: p.isolated, browserRuntime: p.browserRuntime, policy: p.policy}
}

func (g *ControlGuard) release(id string) {
	g.mu.Lock()
	p, ok := g.panes[id]
	delete(g.panes, id)
	for bid, browser := range g.browsers {
		if browser.window == id {
			delete(g.browsers, bid)
		}
	}
	g.mu.Unlock()
	if ok && p.cleanup != nil {
		p.cleanup()
	}
}

// launchArgv creates the real OS envelope before asking the daemon to spawn.
// It never silently falls back to bare execution. The environment is an allow
// list: a daemon's inherited provider tokens must not become pane environment.
func (g *ControlGuard) launchArgv(ctx context.Context, argv []string, cwd string, network bool, runtimeFiles ...string) ([]string, func(), bool, error) {
	return g.launchArgvFor(ctx, argv, cwd, network, false, runtimeFiles...)
}

func (g *ControlGuard) launchArgvFor(ctx context.Context, argv []string, cwd string, network, browser bool, runtimeFiles ...string) ([]string, func(), bool, error) {
	if g.Mode != nil && g.Mode.Mode() == permissions.ReadOnly {
		return nil, nil, false, errors.New("read-only mode cannot launch panes")
	}
	if network && !g.network(ctx) {
		return nil, nil, false, errors.New("pane/browser launch requires reviewed network permission")
	}
	if len(argv) == 0 {
		argv = []string{"bash", "--noprofile", "--norc"}
	}
	if g.full() && !browser && !agentcore.SandboxRequestedFromContext(ctx) {
		return argv, func() {}, false, nil
	}
	browserOutside := browser && (g.full() || runtime.GOOS == "darwin" && agentcore.BrowserRuntimeGrantedFromContext(ctx))
	if browser && runtime.GOOS == "darwin" {
		if agentcore.SandboxRequestedFromContext(ctx) || seatbelt.ModeFromEnv() == seatbelt.ModeEnforce {
			return nil, nil, false, errors.New("Chromium's own sandbox cannot initialize beneath macOS Seatbelt; sandbox enforce refuses a browser runtime exception")
		}
		if !browserOutside {
			return nil, nil, false, errors.New("controlled macOS browser launch needs a one-call browser-runtime decision from the approval gate; Chromium's own sandbox stays enabled")
		}
	}
	if !browserOutside && g.Runner == nil {
		return nil, nil, false, errors.New("no OS sandbox is available for controlled panes; nothing was launched")
	}
	home, err := os.MkdirTemp("", "golder-pane-home-")
	if err != nil {
		return nil, nil, false, err
	}
	cleanHome := func() { _ = os.RemoveAll(home) }
	env, err := exec.LookPath("env")
	if err != nil {
		cleanHome()
		return nil, nil, false, err
	}
	clean := []string{env, "-i", "PATH=" + os.Getenv("PATH"), "HOME=" + home, "TERM=xterm-256color", "TMPDIR=" + os.TempDir()}
	if browser {
		clean = append(clean, "CFFIXED_USER_HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, "config"), "XDG_CACHE_HOME="+filepath.Join(home, "cache"))
	}
	clean = append(clean, argv...)
	if browserOutside {
		return clean, cleanHome, false, nil
	}
	policy := seatbelt.CallPolicy{Network: network, ReadDirs: []string{home}, WriteDirs: []string{home}}
	policy.ReadFiles = append(policy.ReadFiles, runtimeFiles...)
	seatbelt.RefusedPaths(&policy)
	// One-call directory/file grants are decided by consent, never by arguments.
	if p := agentcore.ReadGrantFromContext(ctx); p != "" {
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			policy.ReadDirs = append(policy.ReadDirs, p)
		} else {
			policy.ReadFiles = append(policy.ReadFiles, p)
		}
	}
	if p := agentcore.WriteGrantFromContext(ctx); p != "" {
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			policy.WriteDirs = append(policy.WriteDirs, p)
		} else {
			policy.WriteFiles = append(policy.WriteFiles, p)
		}
	}
	wrapped, cleanup, err := g.Runner.SandboxCall(clean, cwd, policy)
	if err != nil {
		cleanHome()
		return nil, nil, false, fmt.Errorf("create pane sandbox: %w", err)
	}
	return wrapped, func() {
		if cleanup != nil {
			cleanup()
		}
		cleanHome()
	}, true, nil
}

func (g *ControlGuard) launch(ctx context.Context, c *tuiosctl.Client, p tuiosctl.NewWindowParams, network, browser, beside bool) (tuiosctl.NewWindowResult, error) {
	g.launchMu.Lock()
	defer g.launchMu.Unlock()
	// Reap exited helpers and their temporary profiles before applying the cap.
	if err := g.reap(ctx, c); err != nil {
		return tuiosctl.NewWindowResult{}, err
	}
	g.mu.Lock()
	full := len(g.panes) >= 64
	g.mu.Unlock()
	if full {
		return tuiosctl.NewWindowResult{}, errors.New("controlled pane limit reached; close an owned pane first")
	}
	var runtimeFiles []string
	if browser && len(p.Command) > 0 {
		runtimeFiles = []string{p.Command[0]}
	}
	argv, cleanup, isolated, err := g.launchArgvFor(ctx, p.Command, p.Cwd, network, browser, runtimeFiles...)
	if err != nil {
		return tuiosctl.NewWindowResult{}, err
	}
	p.Command = argv
	p.Grants = []string{"none"} // even a network-enabled helper cannot drive the daemon
	var res tuiosctl.NewWindowResult
	if beside {
		res, err = c.NewWindowInCallerWorkspace(ctx, p)
	} else {
		res, err = c.NewWindow(ctx, p)
	}
	if err != nil || res.WindowID == "" {
		cleanup()
		if err == nil {
			err = errors.New("daemon did not identify the new pane")
		}
		return res, err
	}
	g.mu.Lock()
	g.panes[res.WindowID] = controlledPane{cwd: p.Cwd, network: network, isolated: isolated, browser: browser, cleanup: cleanup,
		browserRuntime: browser && !isolated && (g.full() || runtime.GOOS == "darwin" && agentcore.BrowserRuntimeGrantedFromContext(ctx)),
		sealed:         agentcore.ReadGrantFromContext(ctx) != "" || agentcore.WriteGrantFromContext(ctx) != "", policy: controlPolicyStamp()}
	g.mu.Unlock()
	return res, nil
}

func (g *ControlGuard) reap(ctx context.Context, c *tuiosctl.Client) error {
	g.mu.Lock()
	n := len(g.panes)
	g.mu.Unlock()
	if n == 0 {
		return nil
	}
	list, err := c.ListWindows(ctx)
	if err != nil {
		return err
	}
	live := map[string]bool{}
	for _, w := range list.Windows {
		live[w.WindowID] = true
	}
	g.mu.Lock()
	var gone []string
	for id := range g.panes {
		if !live[id] {
			gone = append(gone, id)
		}
	}
	g.mu.Unlock()
	for _, id := range gone {
		g.release(id)
	}
	return nil
}

func (g *ControlGuard) cwd(raw string) (string, error) {
	if raw == "" {
		raw = g.Root
	}
	if !filepath.IsAbs(raw) {
		raw = filepath.Join(g.Root, raw)
	}
	return filepath.EvalSymlinks(raw)
}

// A persistent sandbox cannot be narrowed in place. Refuse more input after
// grants change rather than letting a stale OS profile outlive a revocation.
func controlPolicyStamp() [32]byte {
	p := seatbelt.CallPolicy{ReadDirs: seatbelt.ReadableRoots(), WriteDirs: seatbelt.WritableRoots()}
	seatbelt.RefusedPaths(&p)
	raw, _ := json.Marshal(p)
	return sha256.Sum256(raw)
}

type controlGuardKey struct{}

func controlFloor(ctx context.Context, tool string, raw json.RawMessage) error {
	v := (judge.Chain{}).Classify(ctx, tool, raw)
	if v.Level == judge.Deny {
		return fmt.Errorf("%s blocked by static floor: %s", tool, strings.Join(v.Reasons, "; "))
	}
	return nil
}

func withControlGuard(ctx context.Context, g *ControlGuard) context.Context {
	return context.WithValue(ctx, controlGuardKey{}, g)
}
func controlGuardFrom(ctx context.Context) *ControlGuard {
	g, _ := ctx.Value(controlGuardKey{}).(*ControlGuard)
	return g
}
