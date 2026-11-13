package judge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/redact"
)

// Browser audit notes must not copy arbitrary selector values or credentials
// from reviewer prose. This is defense in depth; page text stays untrusted.
var browserAuditRedactor = func() *redact.Redactor {
	r, err := redact.New(redact.Config{Mode: redact.Redact, Aggressive: true,
		Placeholder: func(string, string) string { return "[REDACTED]" }})
	if err != nil {
		return nil
	}
	return r
}()

func browserAuditRationale(text string) string {
	if text == "" {
		return ""
	}
	if browserAuditRedactor == nil {
		return "[review rationale withheld]"
	}
	return clampText(browserAuditRedactor.Scan(redact.SeamToolOutput, "browser-review", text).Text, 500)
}

// browserReviewAudit intentionally keeps only host identities and bounded
// structural facts, not URLs, page text, input values, or the private binding.
func browserReviewAudit(ctx context.Context, call agentcore.AgentToolCall, note Note) *BrowserReviewAudit {
	ev, ok := agentcore.ToolReviewEvidenceFromContext(ctx, call.Name, call.Arguments)
	if !ok {
		return nil
	}
	audit := &BrowserReviewAudit{RuntimeAttested: ev.RuntimeAttested, OuterSandbox: ev.CanSandbox,
		TargetBound: ev.Binding != "", MatchCount: -1, Level: note.Level.String(), Risk: "unknown", Authorization: "unknown", Coverage: note.ReviewCoverage}
	switch note.Risk {
	case "low", "medium", "high", "critical":
		audit.Risk = note.Risk
	}
	switch note.Authorization {
	case "low", "medium", "high":
		audit.Authorization = note.Authorization
	}
	switch note.BrowserContainment {
	case "remote_effect_only", "outer_required":
		audit.Containment = note.BrowserContainment
	}
	var args struct {
		Action   string `json:"action"`
		Browser  string `json:"browser"`
		Tab      string `json:"tab"`
		TargetID string `json:"target_id"`
	}
	if json.Unmarshal(call.Arguments, &args) == nil {
		if len(args.Action) <= 32 && strings.Trim(args.Action, "abcdefghijklmnopqrstuvwxyz_") == "" {
			audit.Action = args.Action
		}
	}
	var facts struct {
		MatchCount *int   `json:"match_count"`
		Truncated  *bool  `json:"snapshot_truncated"`
		SnapshotID string `json:"snapshot_id"`
	}
	if json.Unmarshal(ev.Facts, &facts) == nil {
		if facts.MatchCount != nil {
			audit.MatchCount = *facts.MatchCount
		}
		audit.SnapshotTruncated = facts.Truncated
		if args.Browser != "" && args.Tab != "" && args.TargetID != "" && facts.SnapshotID != "" {
			identity := sha256.Sum256([]byte(args.Browser + "\x00" + args.Tab + "\x00" + args.TargetID + "\x00" + facts.SnapshotID))
			audit.TargetIDHash = hex.EncodeToString(identity[:8])
		}
	}
	return audit
}

// browserRemoteEffectAllowed is not a general browser exemption. The reviewer
// must explicitly say that its concern is the remote business effect, not a
// requirement to isolate the browser process. The host must also have bound a
// single target on an owned runtime, and no extra capability may be inferred.
func browserRemoteEffectAllowed(ctx context.Context, opts ConsentOpts, call agentcore.AgentToolCall, v Verdict, needs []Need) (agentcore.ToolReviewEvidence, bool) {
	if opts.Enforce || !strings.EqualFold(call.Name, "browser") || v.Failed || v.Source != "llm" ||
		v.Level != Contain || v.BrowserContainment != "remote_effect_only" || len(needs) != 0 ||
		call.RequireOperatorConfirmation || call.RequirePolicyApproval {
		return agentcore.ToolReviewEvidence{}, false
	}
	if v.Coverage == nil || v.Coverage.ArgumentsTruncated || v.Coverage.ExecutionFactsOmitted {
		return agentcore.ToolReviewEvidence{}, false
	}
	switch strings.ToLower(v.Risk) {
	case "low", "medium", "high":
	default:
		return agentcore.ToolReviewEvidence{}, false
	}
	if v.Authorization != "high" && v.Authorization != "medium" {
		return agentcore.ToolReviewEvidence{}, false
	}
	if NetworkRequired(ctx, call.Name, call.Arguments) && !v.Network {
		return agentcore.ToolReviewEvidence{}, false
	}
	var args struct {
		Action      string `json:"action"`
		Browser     string `json:"browser"`
		Tab         string `json:"tab"`
		TargetID    string `json:"target_id"`
		PageURL     string `json:"page_url"`
		HostControl bool   `json:"host_control"`
	}
	if json.Unmarshal(call.Arguments, &args) != nil || args.HostControl || args.Browser == "" || args.Tab == "" || args.TargetID == "" || args.PageURL == "" {
		return agentcore.ToolReviewEvidence{}, false
	}
	switch args.Action {
	case "click", "dblclick", "hover", "focus", "check", "uncheck", "fill", "type", "select", "scroll_into_view":
	default:
		return agentcore.ToolReviewEvidence{}, false
	}
	ev, ok := agentcore.ToolReviewEvidenceFromContext(ctx, call.Name, call.Arguments)
	if !ok || !ev.RuntimeAttested || ev.LaunchRequired || len(ev.Binding) != 64 {
		return agentcore.ToolReviewEvidence{}, false
	}
	if _, err := hex.DecodeString(ev.Binding); err != nil {
		return agentcore.ToolReviewEvidence{}, false
	}
	var facts struct {
		Browser    string `json:"browser"`
		Tab        string `json:"tab"`
		TargetID   string `json:"target_id"`
		PageURL    string `json:"page_url"`
		SnapshotID string `json:"snapshot_id"`
		Truncated  *bool  `json:"snapshot_truncated"`
		MatchCount int    `json:"match_count"`
		Resolution string `json:"resolution"`
	}
	if json.Unmarshal(ev.Facts, &facts) != nil || facts.Browser != args.Browser || facts.Tab != args.Tab ||
		facts.TargetID != args.TargetID || facts.PageURL == "" || facts.SnapshotID == "" ||
		facts.Truncated == nil || *facts.Truncated || facts.MatchCount != 1 ||
		(facts.Resolution != "observed" && facts.Resolution != "observed_css") {
		return agentcore.ToolReviewEvidence{}, false
	}
	return ev, true
}

// browserContainmentBlocked reports only host facts; it does not promise an
// operator override that the current policy and runtime do not implement.
func browserContainmentBlocked(call agentcore.AgentToolCall, v Verdict) *agentcore.BeforeToolCallDecision {
	reason := "browser_outer_sandbox_unavailable"
	message := "browser_outer_sandbox_unavailable: the reviewed call requires outer OS containment and this browser cannot provide it; no input was sent. Do not retry unchanged or add host_control; gather new evidence or use a suitably isolated runtime"
	if v.BrowserContainment == "remote_effect_only" {
		reason = "browser_remote_action_not_eligible"
		message = "browser_remote_action_not_eligible: the reviewer described a remote business effect, but this call did not satisfy the current mode's exact-target or capability requirements; no input was sent. Do not retry unchanged or add host_control"
	}
	dec := blockCall(call, v, message)
	details := any(map[string]any{
		"execution_state": "not_dispatched", "reason": reason,
		"review_level": v.Level.String(), "browser_containment": v.BrowserContainment,
		"recovery": "additional_evidence_or_isolated_runtime",
	})
	dec.Details = &details
	return dec
}

func browserCapabilityBlocked(call agentcore.AgentToolCall, v Verdict, needs []Need) *agentcore.BeforeToolCallDecision {
	var missing []string
	for _, need := range needs {
		switch need.Cap {
		case CapNetwork:
			missing = append(missing, "network")
		case CapHostControl:
			missing = append(missing, "host_control")
		case CapBrowserRuntime:
			missing = append(missing, "browser_runtime")
		default:
			missing = append(missing, "other")
		}
	}
	message := "browser_capability_not_granted: review returned contain and separately required capabilities were not granted: " + strings.Join(missing, ", ") + "; no input was sent. Do not retry unchanged"
	dec := blockCall(call, v, message)
	details := any(map[string]any{"execution_state": "not_dispatched", "reason": "browser_capability_not_granted", "missing": missing, "review_level": v.Level.String(), "browser_containment": v.BrowserContainment, "recovery": "additional_evidence_or_permitted_runtime"})
	dec.Details = &details
	return dec
}
