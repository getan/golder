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
	Ref       string `json:"ref,omitempty"`
	ParentRef string `json:"parentRef,omitempty"`
	Role      string `json:"role,omitempty"`
	Name      string `json:"name,omitempty"`
	Text      string `json:"text,omitempty"`
	Depth     int    `json:"depth,omitempty"`
	Visible   *bool  `json:"visible,omitempty"`
}

type browserObservedNode struct {
	browserReviewNode
	Value any `json:"value,omitempty"` // matching only; never reviewer text
}

type browserReviewFacts struct {
	Browser       string                 `json:"browser"`
	Tab           string                 `json:"tab,omitempty"`
	TargetID      string                 `json:"target_id,omitempty"`
	PageURL       string                 `json:"page_url,omitempty"`
	Backend       string                 `json:"backend"`
	Envelope      map[string]any         `json:"envelope"`
	Locator       map[string]any         `json:"locator,omitempty"`
	SnapshotID    string                 `json:"snapshot_id,omitempty"`
	Truncated     *bool                  `json:"snapshot_truncated,omitempty"`
	MatchCount    int                    `json:"match_count"` // -1 means not known
	Candidates    []browserReviewNode    `json:"candidates,omitempty"`
	Parents       []browserReviewNode    `json:"parents,omitempty"`
	Context       []browserReviewContext `json:"associated_context,omitempty"`
	NameStatus    string                 `json:"target_name_status,omitempty"`
	ContextStatus string                 `json:"context_status,omitempty"`
	Resolution    string                 `json:"resolution"`
	Fingerprint   string                 `json:"-"` // host-private, especially for HTML/input attributes
}

func browserLocatorAction(action string) bool {
	switch action {
	case "click", "dblclick", "hover", "focus", "check", "uncheck", "scroll_into_view", "fill", "type", "select", "upload":
		return true
	}
	return false
}

func browserReviewLocator(a browserArgs) map[string]any {
	if a.Action == "fill" || a.Action == "type" {
		a.Text = "" // payload, not a locator or target evidence
	}
	selector := browserSafeReviewText(a.Selector, 256)
	if strings.Contains(a.Selector, "[") && strings.Contains(a.Selector, "=") {
		selector = "[selector attribute values withheld]"
	}
	return map[string]any{"ref": DisplayBrowserEvidence(a.Ref, 80), "selector": selector,
		"role": DisplayBrowserEvidence(a.Role, 64), "name": browserSafeReviewText(a.Name, 256), "text": browserSafeReviewText(a.Text, 256), "exact": a.Exact}
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
		if a.Ref != "" && facts.MatchCount == 0 && facts.Truncated != nil && !*facts.Truncated {
			return agentcore.ToolReviewEvidence{}, browserNotDispatched("browser_ref_not_in_snapshot", "the ref is not present in the current snapshot; it may be stale or the page may have changed. Take a new snapshot before choosing a target; no input was sent", map[string]any{"source": "host_state", "url": browserSafeReviewURL(a.PageURL)})
		}
	}
	facts.Browser = DisplayBrowserEvidence(facts.Browser, 128)
	facts.Tab = DisplayBrowserEvidence(facts.Tab, 128)
	facts.TargetID = DisplayBrowserEvidence(facts.TargetID, 128)
	facts.PageURL = browserSafeReviewURL(facts.PageURL)
	data, err := json.Marshal(facts)
	return agentcore.ToolReviewEvidence{Facts: data, CanSandbox: canSandbox, Binding: facts.Fingerprint, RuntimeAttested: runtimeAttested, LaunchRequired: launchRequired}, err
}

func browserNotDispatched(code, message string, details any) error {
	data, _ := json.Marshal(details)
	return &browserctl.Error{Code: code, Message: message, ExecutionState: "not_dispatched", Details: data}
}

// These host-generated diagnostics describe the evidence, not the reason the
// reviewer denied a call. No page text or input value is copied into the note.
func browserReviewDenialDiagnostic(ctx context.Context, tool string, args json.RawMessage) string {
	ev, ok := agentcore.ToolReviewEvidenceFromContext(ctx, tool, args)
	if !ok {
		return ""
	}
	var facts browserReviewFacts
	if json.Unmarshal(ev.Facts, &facts) != nil || facts.MatchCount != 1 {
		return ""
	}
	if facts.NameStatus == "not_observed_for_css_locator" {
		return "Target evidence: the CSS locator did not establish whether the control has an accessible name. Context may be unavailable or withheld; this is not proof that no visible label exists or the reason for the decision. No input was dispatched."
	}
	if facts.NameStatus != "no_accessible_name" {
		return ""
	}
	message := "Target evidence: the control has no accessible name. "
	switch facts.ContextStatus {
	case "unnamed_target_visible_descendant_context", "unnamed_target_visible_item_context", "unnamed_target_visible_rendered_text":
		message += "The review included verified visible associated text, separately from the target name. "
	case "unavailable_host_has_no_parent_links":
		message += "This browser host did not provide parent links, so associated text was not included; this does not prove that no visible label exists. "
	case "unavailable_truncated_snapshot":
		message += "The snapshot was truncated, so associated text was not included; this does not prove that no visible label exists. "
	default:
		message += "No verified associated text was included; this does not prove that no visible label exists. "
	}
	return message + "These are evidence limitations, not a claim about the reason for the decision. No input was dispatched."
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
		ID          string                `json:"snapshotId"`
		Truncated   *bool                 `json:"truncated"`
		ParentLinks bool                  `json:"parentLinks"`
		Nodes       []browserObservedNode `json:"nodes"`
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
	matchIndex := -1
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
			matchIndex = i
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
		switch {
		case strings.TrimSpace(fingerprintNode.Name) == "":
			facts.NameStatus = "no_accessible_name"
		case browserSafeReviewText(fingerprintNode.Name, 256) == "[credential-like text withheld]":
			facts.NameStatus = "name_withheld_as_sensitive"
		default:
			facts.NameStatus = "accessible_name_present"
		}
		var contextProof []browserReviewNode
		var directProof []string
		if *snapshot.Truncated {
			facts.ContextStatus = "unavailable_truncated_snapshot"
		} else if !snapshot.ParentLinks {
			facts.ContextStatus = "unavailable_host_has_no_parent_links"
		} else if tree := newBrowserReviewTree(snapshot.Nodes); tree == nil {
			facts.ContextStatus = "unavailable_invalid_parent_links"
		} else {
			for _, parent := range tree.ancestors(matchIndex, 2) {
				parentProof = append(parentProof, snapshot.Nodes[parent].browserReviewNode)
				facts.Parents = append(facts.Parents, boundedReviewNode(snapshot.Nodes[parent].browserReviewNode))
			}
			probeBudget := 6
			probe := func(ref string) (*bool, error) {
				if probeBudget == 0 || ref == "" {
					return nil, nil
				}
				probeBudget--
				raw, meta, err := c.Action(ctx, "is", map[string]any{"kind": "visible", "ref": ref}, target, time.Second)
				if err != nil {
					return nil, err
				}
				if !browserReviewMetaMatches(a, meta) {
					return nil, browserNotDispatched("browser_target_changed", "page changed while checking associated text", nil)
				}
				var value *bool
				if json.Unmarshal(raw, &value) != nil {
					return nil, nil
				}
				return value, nil
			}
			var err error
			facts.Context, facts.ContextStatus, contextProof, err = browserAssociatedContext(tree, matchIndex, probe)
			if err != nil {
				return err
			}
			if facts.NameStatus == "no_accessible_name" && !strings.Contains(facts.ContextStatus, "withheld") {
				if ref := browserReviewSemanticItem(tree, matchIndex); ref != "" {
					text, status, proof, err := browserReviewRenderedText(ctx, c, a, target, map[string]any{"ref": ref}, true)
					if err != nil {
						return err
					}
					directProof = proof
					if status == "target_contains_form_or_unavailable_markup" || text == "[credential-like text withheld]" {
						facts.Context, facts.ContextStatus = nil, "unnamed_item_sensitive_or_editable_context_withheld"
					} else if status == "visible_target_text" && text != "" {
						facts.Context = []browserReviewContext{{Relation: "same_item_rendered_text", Container: DisplayBrowserEvidence(ref, 80), Text: text, Visibility: "visible"}}
						facts.ContextStatus = "unnamed_target_visible_item_context"
					} else if browserReviewItemHasAuxiliaryControls(tree, ref) {
						// Link labels alone are not sufficient to identify a checkbox.
						facts.Context, facts.ContextStatus = nil, "unnamed_target_no_verified_visible_context"
					}
				}
			}
		}
		// The exact unnamed container is safe to inspect even on hosts with
		// parent links; association with a separate item still requires those links.
		if !*snapshot.Truncated && facts.NameStatus == "no_accessible_name" &&
			(facts.ContextStatus == "unavailable_host_has_no_parent_links" || facts.ContextStatus == "unnamed_target_no_verified_visible_context" || facts.ContextStatus == "unnamed_target_visible_descendant_context") &&
			(strings.EqualFold(fingerprintNode.Role, "generic") || strings.EqualFold(fingerprintNode.Role, "group")) && fingerprintNode.Ref != "" {
			text, status, proof, err := browserReviewTargetText(ctx, c, a, target, map[string]any{"ref": fingerprintNode.Ref})
			if err != nil {
				return err
			}
			directProof = proof
			if status == "target_contains_form_or_unavailable_markup" || text == "[credential-like text withheld]" {
				facts.Context, facts.ContextStatus = nil, "unnamed_target_sensitive_or_editable_context_withheld"
			} else if status == "visible_target_text" && text != "" {
				facts.Context = []browserReviewContext{{Relation: "target_rendered_text", Container: DisplayBrowserEvidence(fingerprintNode.Ref, 80), Text: text, Visibility: "visible"}}
				facts.ContextStatus = "unnamed_target_visible_rendered_text"
			}
		}
		facts.Fingerprint = browserTargetFingerprint(snapshot.ID, []any{fingerprintNode, parentProof, facts.ContextStatus, contextProof, directProof})
	}
	return nil
}

func boundedReviewNode(node browserReviewNode) browserReviewNode {
	node.Ref = DisplayBrowserEvidence(node.Ref, 80)
	node.ParentRef = DisplayBrowserEvidence(node.ParentRef, 80)
	if node.Visible != nil && !*node.Visible {
		node.Name = ""
	} else {
		node.Name = browserSafeReviewText(node.Name, 256)
	}
	node.Role = DisplayBrowserEvidence(node.Role, 64)
	node.Text = "" // accessible name is not descendant or neighbouring text
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
	facts.NameStatus = "not_observed_for_css_locator"
	text, status, proof, err := browserReviewTargetText(ctx, c, a, target, map[string]any{"selector": a.Selector})
	if err != nil {
		return err
	}
	facts.ContextStatus = "css_" + status
	if status == "visible_target_text" {
		facts.Candidates = []browserReviewNode{{Text: text}}
	}
	facts.Fingerprint = browserTargetFingerprint(facts.SnapshotID, []any{proof, facts.ContextStatus})
	return nil
}

// Read only the exact target's rendered text, never its inputs or an enclosing
// form. The markup and raw results are kept private and bound to the review.
func browserReviewTargetText(ctx context.Context, c *browserctl.Client, a browserArgs, target browserctl.Target, locator map[string]any) (string, string, []string, error) {
	return browserReviewRenderedText(ctx, c, a, target, locator, false)
}

func browserReviewRenderedText(ctx context.Context, c *browserctl.Client, a browserArgs, target browserctl.Target, locator map[string]any, item bool) (string, string, []string, error) {
	call := func(action, kind string) (json.RawMessage, error) {
		params := make(map[string]any, len(locator)+1)
		for key, value := range locator {
			params[key] = value
		}
		params["kind"] = kind
		raw, meta, err := c.Action(ctx, action, params, target, 2*time.Second)
		if err == nil && !browserReviewMetaMatches(a, meta) {
			return nil, browserNotDispatched("browser_target_changed", "page changed while inspecting the exact target", nil)
		}
		return raw, err
	}
	html, err := call("get", "html")
	if err != nil {
		return "", "", nil, err
	}
	proof := []string{string(html)}
	var markup string
	if json.Unmarshal(html, &markup) != nil {
		return "", "markup_unavailable", proof, nil
	}
	if (!item && !browserReviewSafeMarkup(markup)) || (item && !browserReviewSafeContainerMarkup(markup)) {
		return "", "target_contains_form_or_unavailable_markup", proof, nil
	}
	visibility, err := call("is", "visible")
	if err != nil {
		return "", "", nil, err
	}
	proof = append(proof, string(visibility))
	var visible bool
	if json.Unmarshal(visibility, &visible) != nil || !visible {
		return "", "visibility_unavailable_or_hidden", proof, nil
	}
	text, err := call("get", "text")
	if err != nil {
		return "", "", nil, err
	}
	proof = append(proof, string(text))
	var content string
	if json.Unmarshal(text, &content) != nil {
		return "", "target_text_unavailable", proof, nil
	}
	return browserSafeReviewText(content, 256), "visible_target_text", proof, nil
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
	// An unnamed mutating control cannot be tied to its meaning across the
	// review unless the review actually saw bound, associated context. A nearby
	// string alone is not authorization; the ordinary reviewer still decides.
	needsMeaning := a.Action == "click" || a.Action == "dblclick" || a.Action == "check" || a.Action == "uncheck"
	if needsMeaning && len(previous.Candidates) == 1 && previous.Candidates[0].Visible != nil && !*previous.Candidates[0].Visible {
		return browserNotDispatched("browser_target_not_visible", "the reviewed target is hidden; inspect the page rather than clicking another candidate", previous)
	}
	missingAXContext := previous.NameStatus == "no_accessible_name" && len(previous.Context) == 0
	missingCSSContext := previous.NameStatus == "not_observed_for_css_locator" &&
		(len(previous.Candidates) != 1 || previous.Candidates[0].Text == "" || previous.Candidates[0].Text == "[credential-like text withheld]")
	if needsMeaning && (missingAXContext || missingCSSContext) {
		return browserNotDispatched("browser_target_context_unavailable", "target has no accessible name or verified visible context; inspect the page and narrow to an element with its visible label; no input was sent", previous)
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
