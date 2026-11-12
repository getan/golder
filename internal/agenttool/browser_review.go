package agenttool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/browserctl"
)

type browserReviewNode struct {
	Ref     string `json:"ref,omitempty"`
	Role    string `json:"role,omitempty"`
	Name    string `json:"name,omitempty"`
	Text    string `json:"text,omitempty"`
	Depth   int    `json:"depth,omitempty"`
	Visible *bool  `json:"visible,omitempty"`
}

type browserObservedNode struct {
	browserReviewNode
	Value any `json:"value,omitempty"` // matching only; never reviewer text
}

type browserReviewFacts struct {
	Browser     string              `json:"browser"`
	Tab         string              `json:"tab,omitempty"`
	TargetID    string              `json:"target_id,omitempty"`
	PageURL     string              `json:"page_url,omitempty"`
	Backend     string              `json:"backend"`
	Envelope    map[string]any      `json:"envelope"`
	Locator     map[string]any      `json:"locator,omitempty"`
	SnapshotID  string              `json:"snapshot_id,omitempty"`
	Truncated   *bool               `json:"snapshot_truncated,omitempty"`
	MatchCount  int                 `json:"match_count"` // -1 means not known
	Candidates  []browserReviewNode `json:"candidates,omitempty"`
	Parents     []browserReviewNode `json:"parents,omitempty"`
	Resolution  string              `json:"resolution"`
	Fingerprint string              `json:"-"` // host-private, especially for HTML/input attributes
}

func browserLocatorAction(action string) bool {
	switch action {
	case "click", "dblclick", "hover", "focus", "check", "uncheck", "scroll_into_view", "fill", "type", "select":
		return true
	}
	return false
}

func browserReviewLocator(a browserArgs) map[string]any {
	if a.Action == "fill" || a.Action == "type" {
		a.Text = "" // payload, not a locator or target evidence
	}
	return map[string]any{"ref": DisplayBrowserEvidence(a.Ref, 80), "selector": DisplayBrowserEvidence(a.Selector, 256),
		"role": DisplayBrowserEvidence(a.Role, 64), "name": DisplayBrowserEvidence(a.Name, 256), "text": DisplayBrowserEvidence(a.Text, 256), "exact": a.Exact}
}

// ReviewEvidence observes the current target directly. It never relies on the
// reviewer's truncated transcript and never performs input/navigation. Browser
// ownership and runtime facts are host observations; page labels remain data.
func (t *BrowserTool) ReviewEvidence(ctx context.Context, args json.RawMessage) (agentcore.ToolReviewEvidence, error) {
	var a browserArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return agentcore.ToolReviewEvidence{}, err
	}
	facts := browserReviewFacts{Browser: a.Browser, Tab: a.Tab, TargetID: a.TargetID, PageURL: a.PageURL,
		Backend: "unknown", MatchCount: -1, Resolution: "no_locator"}
	socket := t.socketPath()
	if t.Control == nil {
		return agentcore.ToolReviewEvidence{}, errors.New("browser control policy is not configured")
	}
	t.Control.mu.Lock()
	owned, found := t.Control.browsers[a.Browser]
	t.Control.mu.Unlock()
	facts.Envelope = browserEnvelopeFacts(owned, found, socket)
	runtimeAttested := found && owned.socket == canonicalBrowserSocket(socket) && owned.policy == controlPolicyStamp() && (owned.isolated || owned.browserRuntime)
	launchRequired := a.Action == "open" && (!runtimeAttested || a.NewInstance)
	facts.Envelope["runtime_attested"], facts.Envelope["launch_required"] = runtimeAttested, launchRequired
	canSandbox := found && owned.socket == canonicalBrowserSocket(socket) && owned.isolated && owned.policy == controlPolicyStamp()
	if a.Action == "screenshot" {
		canSandbox = false // artifact writes occur in the host, not the browser envelope
	}
	if a.Action == "open" && (a.NewInstance || a.Browser == "") {
		canSandbox = runtime.GOOS != "darwin" && !NativeBrowserLauncherAvailable() && t.Control.Runner != nil
		facts.Resolution = "launch"
	} else if browserLocatorAction(a.Action) {
		if a.Socket != "" && a.Socket != socket {
			return agentcore.ToolReviewEvidence{}, browserNotDispatched("browser_binding_changed", "browser binding changed before target review", nil)
		}
		c, err := t.clientForSocket(socket)
		if err != nil {
			return agentcore.ToolReviewEvidence{}, err
		}
		defer c.CloseIdleConnections()
		info, err := c.Info(ctx)
		if err != nil {
			return agentcore.ToolReviewEvidence{}, err
		}
		if info.BrowserID != a.Browser || t.checkBinding(info) != nil {
			return agentcore.ToolReviewEvidence{}, browserNotDispatched("browser_binding_changed", "browser identity changed before target review", nil)
		}
		if err := t.checkProfileAccess(info); err != nil {
			return agentcore.ToolReviewEvidence{}, err
		}
		facts.Backend = info.Backend
		if err := t.observeReviewTarget(ctx, c, a, &facts); err != nil {
			return agentcore.ToolReviewEvidence{}, err
		}
	}
	facts.Browser = DisplayBrowserEvidence(facts.Browser, 128)
	facts.Tab = DisplayBrowserEvidence(facts.Tab, 128)
	facts.TargetID = DisplayBrowserEvidence(facts.TargetID, 128)
	facts.PageURL = DisplayBrowserEvidence(facts.PageURL, 1024)
	data, err := json.Marshal(facts)
	return agentcore.ToolReviewEvidence{Facts: data, CanSandbox: canSandbox, Binding: facts.Fingerprint, RuntimeAttested: runtimeAttested, LaunchRequired: launchRequired}, err
}

func browserNotDispatched(code, message string, details any) error {
	data, _ := json.Marshal(details)
	return &browserctl.Error{Code: code, Message: message, ExecutionState: "not_dispatched", Details: data}
}

func browserReviewMetaMatches(a browserArgs, meta browserctl.Meta) bool {
	return meta.BrowserID == a.Browser && meta.TabID == a.Tab && meta.TargetID == a.TargetID && meta.URL == a.PageURL
}

func (t *BrowserTool) observeReviewTarget(ctx context.Context, c *browserctl.Client, a browserArgs, facts *browserReviewFacts) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	target := browserctl.Target{BrowserID: a.Browser, TabID: a.Tab, TargetID: a.TargetID}
	status, _, err := c.Action(ctx, "interaction.status", map[string]any{}, browserctl.Target{BrowserID: a.Browser}, 2*time.Second)
	if err != nil {
		return err
	}
	var state struct {
		Paused          bool   `json:"paused"`
		PauseGeneration uint64 `json:"pauseGeneration"`
	}
	_ = json.Unmarshal(status, &state)
	if state.Paused {
		return browserNotDispatched("browser_paused", "browser is paused; use its local Resume automation button once the required step is complete", map[string]any{"browser": a.Browser, "pause_generation": state.PauseGeneration})
	}
	raw, meta, err := c.Action(ctx, "snapshot", map[string]any{"maxNodes": 10000, "includeText": false}, target, 3*time.Second)
	if err != nil {
		return err
	}
	if !browserReviewMetaMatches(a, meta) {
		return browserNotDispatched("browser_target_changed", "page or tab changed while observing the target; no input was sent", nil)
	}
	var snapshot struct {
		ID        string                `json:"snapshotId"`
		Truncated *bool                 `json:"truncated"`
		Nodes     []browserObservedNode `json:"nodes"`
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil || snapshot.ID == "" {
		facts.Resolution = "snapshot_identity_unavailable"
		return nil // no invented evidence or containment capability
	}
	facts.SnapshotID, facts.Truncated = DisplayBrowserEvidence(snapshot.ID, 128), snapshot.Truncated
	if snapshot.Truncated == nil {
		facts.Resolution = "snapshot_completeness_unavailable"
		return nil
	}
	facts.Locator = browserReviewLocator(a)
	if a.Ref == "" && a.Selector != "" {
		return observeCSSReviewTarget(ctx, c, a, target, facts)
	}
	match := func(hay, needle string) bool {
		if needle == "" {
			return true
		}
		if a.Exact {
			return hay == needle
		}
		return strings.Contains(strings.ToLower(hay), strings.ToLower(needle))
	}
	count := 0
	var fingerprintNode browserReviewNode
	var parentProof []browserReviewNode
	for i, node := range snapshot.Nodes {
		matches := strings.TrimPrefix(node.Ref, "@") == strings.TrimPrefix(a.Ref, "@")
		if a.Ref == "" {
			text := a.Text
			if a.Action == "fill" || a.Action == "type" {
				text = ""
			}
			matches = (a.Role == "" || strings.EqualFold(node.Role, a.Role)) && match(node.Name, a.Name) &&
				(match(node.Name, text) || match(node.Name+" "+fmt.Sprint(node.Value), text))
		}
		if !matches {
			continue
		}
		count++
		if count == 1 {
			fingerprintNode = node.browserReviewNode
			depth := node.Depth
			for parent := i - 1; parent >= 0 && len(facts.Parents) < 2; parent-- {
				if snapshot.Nodes[parent].Depth < depth {
					parentProof = append(parentProof, snapshot.Nodes[parent].browserReviewNode)
					facts.Parents = append(facts.Parents, boundedReviewNode(snapshot.Nodes[parent].browserReviewNode))
					depth = snapshot.Nodes[parent].Depth
				}
			}
		}
		if len(facts.Candidates) < 3 {
			facts.Candidates = append(facts.Candidates, boundedReviewNode(node.browserReviewNode))
		}
	}
	facts.MatchCount = count
	if *snapshot.Truncated && a.Ref == "" {
		facts.MatchCount, facts.Resolution = -1, "snapshot_truncated"
		return nil
	}
	facts.Resolution = "observed"
	if count == 1 {
		facts.Fingerprint = browserTargetFingerprint(snapshot.ID, []any{fingerprintNode, parentProof})
	}
	return nil
}

func boundedReviewNode(node browserReviewNode) browserReviewNode {
	node.Name = DisplayBrowserEvidence(node.Name, 256)
	node.Role = DisplayBrowserEvidence(node.Role, 64)
	node.Text = DisplayBrowserEvidence(node.Name, 256) // never include input values/passwords
	return node
}

func DisplayBrowserEvidence(text string, limit int) string {
	runes := []rune(cleanBrowserEvidence(text))
	if len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return string(runes)
}

func cleanBrowserEvidence(text string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return ' '
		}
		return r
	}, text)
}

func browserTargetFingerprint(document string, node any) string {
	data, _ := json.Marshal([]any{document, node})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func observeCSSReviewTarget(ctx context.Context, c *browserctl.Client, a browserArgs, target browserctl.Target, facts *browserReviewFacts) error {
	get := func(kind string) (json.RawMessage, error) {
		raw, meta, err := c.Action(ctx, "get", map[string]any{"kind": kind, "selector": a.Selector}, target, 2*time.Second)
		if err == nil && !browserReviewMetaMatches(a, meta) {
			return nil, browserNotDispatched("browser_target_changed", "page changed while inspecting locator", nil)
		}
		return raw, err
	}
	raw, err := get("count")
	if err != nil {
		return err
	}
	if json.Unmarshal(raw, &facts.MatchCount) != nil {
		facts.MatchCount = -1
		facts.Resolution = "locator_count_unavailable"
		return nil
	}
	facts.Resolution = "observed_css"
	if facts.MatchCount != 1 {
		return nil
	}
	text, err := get("text")
	if err != nil {
		return err
	}
	var visible string
	_ = json.Unmarshal(text, &visible)
	facts.Candidates = []browserReviewNode{{Text: DisplayBrowserEvidence(visible, 256)}}
	html, err := get("html")
	if err != nil {
		return err
	}
	facts.Fingerprint = browserTargetFingerprint(facts.SnapshotID, string(html)) // raw HTML is never review text
	return nil
}

func (t *BrowserTool) verifyReviewedLocator(ctx context.Context, c *browserctl.Client, a browserArgs, raw json.RawMessage, params map[string]any) error {
	evidence, ok := agentcore.ToolReviewEvidenceFromContext(ctx, t.Name(), raw)
	if !ok || !browserLocatorAction(a.Action) {
		return nil
	}
	var previous browserReviewFacts
	if json.Unmarshal(evidence.Facts, &previous) != nil {
		return browserNotDispatched("browser_evidence_unavailable", "invalid target review evidence", nil)
	}
	previous.Fingerprint = evidence.Binding
	if previous.MatchCount != 1 || previous.Fingerprint == "" {
		code := "browser_evidence_unavailable"
		if previous.MatchCount > 1 {
			code = "ambiguous_locator"
		}
		if previous.MatchCount == 0 {
			code = "not_found"
		}
		return browserNotDispatched(code, "target was not uniquely established before review; narrow the locator", previous)
	}
	current := browserReviewFacts{MatchCount: -1}
	if err := t.observeReviewTarget(ctx, c, a, &current); err != nil {
		return err
	}
	if current.MatchCount != 1 || current.Fingerprint != previous.Fingerprint || current.SnapshotID != previous.SnapshotID {
		return browserNotDispatched("browser_target_changed", "document or locator target changed after review; no input was sent", current)
	}
	if a.Selector == "" && len(current.Candidates) == 1 && current.Candidates[0].Ref != "" {
		for _, key := range []string{"selector", "role", "name"} {
			delete(params, key)
		}
		if a.Action != "fill" && a.Action != "type" {
			delete(params, "text")
		}
		params["ref"] = current.Candidates[0].Ref // stable ref rejects a replacement document in the host
	}
	return nil
}
