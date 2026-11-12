package agenttool

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"runtime"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/browsercontinuation"
	"github.com/getan/golder/internal/browserctl"
	"github.com/getan/golder/internal/browserpool"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/seatbelt"
	"github.com/getan/kitty-browse/browserhost"
)

type continuationHost struct {
	view    *browserhost.RemoteView
	tool    *BrowserTool
	session string
	checked bool
}

func (h continuationHost) valid(ctx context.Context) bool {
	g := h.tool.Control
	return g != nil && g.conversation() == h.session && h.session != "" && g.Mode != nil && g.Mode.Mode() != permissions.ReadOnly && g.ownsBrowser(ctx, h.view.BrowserID, h.view.Socket)
}

func (h continuationHost) State(ctx context.Context) (browserhost.InteractionState, error) {
	if !h.valid(ctx) {
		return browserhost.InteractionState{}, browsercontinuation.ErrRevoked
	}
	c, err := h.tool.clientForSocket(h.view.Socket)
	if err != nil {
		return browserhost.InteractionState{}, err
	}
	defer c.CloseIdleConnections()
	raw, _, err := c.Action(ctx, "interaction.status", map[string]any{}, browserctl.Target{BrowserID: h.view.BrowserID}, time.Second)
	var state browserhost.InteractionState
	if err == nil {
		err = json.Unmarshal(raw, &state)
	}
	if !h.checked {
		state.ContinuationVersion = 0
	}
	return state, err
}

func (h continuationHost) ContinueChecked(ctx context.Context, req browserhost.ContinueRequest) error {
	if !h.valid(ctx) {
		return browsercontinuation.ErrRevoked
	}
	if !h.checked {
		return errors.New("browser_runtime_capability_missing: upgrade the browser host")
	}
	return h.view.ContinueChecked(ctx, req)
}

func (h continuationHost) ContinuationOutcome(ctx context.Context, req browserhost.ContinueRequest) (string, error) {
	if !h.valid(ctx) {
		return "stale", browsercontinuation.ErrRevoked
	}
	return h.view.ContinuationOutcome(ctx, req)
}

// BrowserContinuations returns presentation/decision state only to trusted
// frontends. It is not registered as an AgentTool.
func BrowserContinuations(tools []agentcore.AgentTool) *browsercontinuation.Coordinator {
	for _, raw := range tools {
		if t, ok := raw.(*BrowserTool); ok && t.Control != nil {
			g := t.Control
			g.mu.Lock()
			if g.continuations == nil {
				g.continuations = browsercontinuation.New(g.browserSession)
			}
			c := g.continuations
			g.mu.Unlock()
			return c
		}
	}
	return nil
}

func (t *BrowserTool) registerContinuation(ctx context.Context) {
	g := t.Control
	if g == nil || g.conversation() == "" {
		return
	}
	view, err := ConnectSessionBrowser(ctx, t.cwd, g.conversation(), t.socketPath())
	if err != nil {
		return
	}
	proof, err := view.Attest(ctx)
	if err != nil {
		// A previously owned old host remains observable, never recoverable via
		// its unchecked public resume endpoint. Do not invent new ownership.
		if g.ownsBrowser(ctx, view.BrowserID, view.Socket) {
			BrowserContinuations([]agentcore.AgentTool{t}).Register(view.Socket, view.BrowserID, view.BrowserID, "", continuationHost{view: view, tool: t, session: g.conversation()})
		}
		return
	}
	if !proof.ChromiumSandbox || t.checkProfileAccess(browserctl.Info{LoginProfile: proof.LoginProfile}) != nil {
		return
	}
	g.mu.Lock()
	_, owned := g.browsers[proof.BrowserID]
	g.mu.Unlock()
	if !owned {
		return // Observation/metadata alone does not create ownership.
	}
	c := BrowserContinuations([]agentcore.AgentTool{t})
	origin := ""
	checked := false
	if client, e := t.clientForSocket(view.Socket); e == nil {
		info, e := client.Info(ctx)
		client.CloseIdleConnections()
		if e == nil {
			checked = info.HasCapability("interaction.status.v2")
			for _, tab := range info.Tabs {
				if tab.Active {
					if u, e := url.Parse(tab.URL); e == nil {
						origin = u.Scheme + "://" + u.Host
					}
				}
			}
		}
	}
	c.Register(view.Socket, proof.BrowserID, proof.InstanceEpoch, origin, continuationHost{view: view, tool: t, session: g.conversation(), checked: checked})
}

// Restoring a genuine private view never restores an old execution grant.
// An unrecorded runtime must first pass this call's current environment review.
func (t *BrowserTool) restoreManagedView(ctx context.Context, instance browserInstance, profile string) error {
	g := t.Control
	session := g.conversation()
	if session == "" || !g.profileAllowed(profile) {
		return errors.New("browser_ownership_unverified: no authorized conversation/profile binding")
	}
	if agentcore.SandboxRequestedFromContext(ctx) || seatbelt.ModeFromEnv() == seatbelt.ModeEnforce {
		return errors.New("browser_outer_sandbox_unavailable: an existing runtime cannot move into the requested OS sandbox")
	}
	if runtime.GOOS == "darwin" && !g.full() && !agentcore.BrowserRuntimeGrantedFromContext(ctx) {
		return errors.New("browser_ownership_unverified: reconnected runtime still needs a current browser-runtime review")
	}
	view, err := ConnectSessionBrowser(ctx, t.cwd, session, instance.Socket)
	if err != nil {
		return errors.New("browser_ownership_unverified: private conversation reconnect failed")
	}
	proof, err := view.Attest(ctx)
	if err != nil || !proof.ChromiumSandbox || proof.RuntimeEnvelope != "browser_runtime" || proof.BrowserID != instance.Info.BrowserID || proof.LoginProfile != browserLoginProfile(t.cwd, profile) {
		return errors.New("browser_ownership_unverified: broker could not attest this exact view/runtime")
	}
	path, err := persistentBrowserProfile(t.cwd, profile, false)
	if err != nil {
		return err
	}
	proxy := ""
	if g.BrowserProxy != nil {
		proxy, err = g.BrowserProxy()
		if err != nil {
			return err
		}
	}
	opts, err := browserpool.Options(path, proof.LoginProfile, proxy)
	if err != nil || proof.RuntimePolicyDigest != browserhost.RuntimePolicyDigest(opts.Browser) {
		return errors.New("browser_policy_changed: broker runtime does not match the current profile/proxy/sandbox policy")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.browserSession != session || ctx.Err() != nil {
		return browsercontinuation.ErrRevoked
	}
	g.browsers[proof.BrowserID] = controlledBrowser{socket: canonicalBrowserSocket(view.Socket), profile: profile, proxyMode: proof.ProxyMode, browserRuntime: true, policy: controlPolicyStamp()}
	return nil
}
