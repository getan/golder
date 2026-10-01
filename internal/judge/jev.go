package judge

// JevJudge grades a tool call with the Jev high-speed classifier
// (Typesafe SystemOne, jev-latest) instead of a hand-maintained rule list.
// Rules do not scale: every new dangerous pattern is another regex to argue
// about. Jev takes one choice question over four risk tiers and returns a
// calibrated confidence; code then enforces per-tier thresholds scaled to
// the cost of being wrong.
//
// Chain of responsibility inside Classify:
//
//  1. No key (TYPESAFE_API_KEY unset) → Allow with a "no grader configured"
//     reason. Zero-config runs behave exactly as before (the trust gate and
//     the static floor still apply); only key-configured runs pay for and
//     wait on classification.
//  2. One choice call (allow/confirm/sandbox/deny) with the tool name,
//     truncated arguments, and the directory-trust note as state.
//  3. Low confidence escalates one tier toward Deny. Allow additionally
//     requires high confidence; anything ambiguous lands on Confirm or
//     higher, never on Allow.
//  4. Any failure (timeout, transport error, malformed answer) degrades to
//     Confirm — fail closed, never fail open. Failures are never cached;
//     only successful Jev verdicts are cached per (tool, args, trust-note).
//
// The key travels only from the environment into the Authorization header.
// It is never logged, never echoed into verdict reasons, and never appears
// in error text (failures report a generic reason).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Jev defaults. Endpoint/model are overridable for tests and self-hosted
// relays; the key is always TYPESAFE_API_KEY.
const (
	jevDefaultEndpoint = "https://api.typesafe.ai/v1/systemone"
	jevDefaultModel    = "jev-latest"
	jevDefaultTimeout  = 8 * time.Second
	jevMaxStateRunes   = 1200
)

// allowConfidence is the bar for a frictionless run: Jev must pick "allow"
// with at least this confidence, otherwise the call escalates to Confirm.
// confirmConfidence is the bar for staying at confirm/sandbox; below it the
// verdict escalates one tier toward Deny. Deny never downgrades on
// confidence — a confident-looking "probably fine" must not override it.
// Thresholds are per question type and must not be copied between choice
// and noul/score questions (their probabilities are not interchangeable).
const (
	allowConfidence   = 0.75
	confirmConfidence = 0.60
)

// JevJudge is the Classifier implementation over the SystemOne API. The zero
// value is usable: endpoint/model/timeout resolve from the environment with
// sane defaults. Cwd and DirTrust optionally describe the launch directory
// ("trusted", "untrusted", or "" when unknown) so the model sees the same
// trust context the trust gate enforces. HTTP is a package var so tests can
// substitute the transport without touching the network.
type JevJudge struct {
	Endpoint string
	Model    string
	Timeout  time.Duration
	Cwd      string
	DirTrust string
	// APIKey overrides TYPESAFE_API_KEY. It exists for hermetic tests only;
	// production always reads the environment.
	APIKey string
	// HTTP, when non-nil, replaces http.DefaultClient (tests).
	HTTP *http.Client
}

// NewJevJudge resolves endpoint/model/timeout from the environment.
func NewJevJudge() *JevJudge { return &JevJudge{} }

// ClassifierForCwd wires the static floor to a Jev grader whose state notes
// whether cwd is trusted, so the model grades identically-situated calls
// differently in trusted vs untrusted checkouts.
func ClassifierForCwd(cwd string, trusted bool) Classifier {
	note := "untrusted"
	if trusted {
		note = "trusted"
	}
	return Chain{Inner: &JevJudge{Cwd: cwd, DirTrust: note}}
}

// GraderConfigured reports whether a Jev call can be made: grading is not
// disabled via PIGO_JUDGE=off and a key is present. Wiring uses it to keep
// zero-config runs allocation- and latency-free (Judge stays nil, behavior
// is exactly today's plus the static floor).
func GraderConfigured() bool {
	if !GateEnabled() {
		return false
	}
	return strings.TrimSpace(apiKey()) != ""
}

func apiKey() string { return strings.TrimSpace(os.Getenv("TYPESAFE_API_KEY")) }

func (j *JevJudge) endpoint() string {
	if s := strings.TrimSpace(j.Endpoint); s != "" {
		return s
	}
	if s := strings.TrimSpace(os.Getenv("PIGO_JEV_ENDPOINT")); s != "" {
		return s
	}
	return jevDefaultEndpoint
}

func (j *JevJudge) model() string {
	if s := strings.TrimSpace(j.Model); s != "" {
		return s
	}
	if s := strings.TrimSpace(os.Getenv("PIGO_JEV_MODEL")); s != "" {
		return s
	}
	return jevDefaultModel
}

func (j *JevJudge) timeout() time.Duration {
	if j.Timeout > 0 {
		return j.Timeout
	}
	if s := strings.TrimSpace(os.Getenv("PIGO_JEV_TIMEOUT_MS")); s != "" {
		if ms, err := strconv.Atoi(s); err == nil && ms >= 1000 && ms <= 30000 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return jevDefaultTimeout
}

func (j *JevJudge) key() string {
	if j.APIKey != "" {
		return j.APIKey
	}
	return apiKey()
}

func (j *JevJudge) client() *http.Client {
	if j.HTTP != nil {
		return j.HTTP
	}
	return http.DefaultClient
}

// verdictCache memoizes successful Jev verdicts per (tool, args, trust-note)
// so the BeforeToolCall gate and the execution layer (bash sandbox routing)
// grade the same call once. Failures are never stored.
var verdictCache sync.Map // string -> Verdict

func verdictCacheKey(tool string, args json.RawMessage, trust string) string {
	return cacheKey(tool, args) + "\x00" + trust
}

func cachedVerdict(tool string, args json.RawMessage, trust string) (Verdict, bool) {
	if v, ok := verdictCache.Load(verdictCacheKey(tool, args, trust)); ok {
		if verdict, ok := v.(Verdict); ok {
			return verdict, true
		}
	}
	return Verdict{}, false
}

func storeVerdict(tool string, args json.RawMessage, trust string, v Verdict) {
	verdictCache.Store(verdictCacheKey(tool, args, trust), v)
}

// clearVerdictCache drops all cached verdicts. Tests only.
func clearVerdictCache() {
	verdictCache = sync.Map{}
}

// Classify implements Classifier.
func (j *JevJudge) Classify(ctx context.Context, tool string, args json.RawMessage) Verdict {
	if j == nil {
		return Verdict{Level: Allow, Source: "static", Reasons: []string{"no grader configured"}}
	}
	if cached, ok := cachedVerdict(tool, args, j.DirTrust); ok {
		return cached
	}
	if strings.TrimSpace(j.key()) == "" {
		return Verdict{Level: Allow, Source: "static", Reasons: []string{"no grader configured"}}
	}
	choice, conf, known, ok := j.ask(ctx, tool, args)
	if !ok {
		return Verdict{Level: Confirm, Source: "jev", Reasons: []string{"jev unavailable, failing closed to confirm"}}
	}
	level := escalate(choice, conf)
	if !known {
		// The model answered outside the four-tier vocabulary: fail closed
		// at Confirm without running the confidence escalation, which
		// would otherwise promote a zero-confidence Confirm to Sandbox.
		level = Confirm
	}
	v := Verdict{
		Level:      level,
		Source:     "jev",
		Confidence: conf,
		Reasons:    []string{fmt.Sprintf("jev: %s (confidence %.2f)", level, conf)},
	}
	storeVerdict(tool, args, j.DirTrust, v)
	return v
}

// escalate maps a (choice, confidence) pair onto the enforced tier. Low
// confidence moves one step toward Deny; Deny itself never moves down.
func escalate(choice Level, conf float64) Level {
	switch choice {
	case Allow:
		if conf < allowConfidence {
			return Confirm
		}
		return Allow
	case Confirm:
		if conf < confirmConfidence {
			return Sandbox
		}
		return Confirm
	case Sandbox:
		if conf < confirmConfidence {
			return Deny
		}
		return Sandbox
	default:
		return Deny
	}
}

// jevState renders the model state: tool name, directory-trust note, and the
// arguments truncated to jevMaxStateRunes so a giant command cannot blow the
// grading budget.
func (j *JevJudge) jevState(tool string, args json.RawMessage) string {
	var b strings.Builder
	b.WriteString("tool: ")
	b.WriteString(strings.ToLower(strings.TrimSpace(tool)))
	b.WriteString("\ndirectory-trust: ")
	if j.DirTrust != "" {
		b.WriteString(j.DirTrust)
	} else {
		b.WriteString("unknown")
	}
	if j.Cwd != "" {
		b.WriteString("\ncwd: ")
		b.WriteString(truncateRunes(j.Cwd, 200))
	}
	b.WriteString("\nargs: ")
	b.WriteString(truncateRunes(strings.TrimSpace(string(args)), jevMaxStateRunes))
	return b.String()
}

var jevTiers = []string{"allow", "confirm", "sandbox", "deny"}

var jevCriteria = map[string]any{
	"allow":   "read-only or fully reversible project-scoped action: listing files, running tests, linters or builds, editing files inside the project",
	"confirm": "state-changing but recoverable: installs, version-control writes, single-file deletes inside the project, outbound network fetches",
	"sandbox": "risky or hard to reverse: recursive deletes, permission changes, piping remote content into a shell, broad network or exfiltration surface",
	"deny":    "catastrophic or credential-threatening: privilege escalation, disk wipes, host power control, touching SSH or GPG identity material",
}

func jevQuestion() map[string]any {
	return map[string]any{
		"risk": map[string]any{
			"type":         "choice",
			"options":      jevTiers,
			"criteria":     jevCriteria,
			"instructions": "Grade the risk of this pigo tool call. Prefer the lowest tier that fits. Be strict with shell commands that combine download and execution.",
		},
	}
}

// ask performs one SystemOne choice call. ok=false on any failure
// (transport, timeout, malformed answer): the caller fails closed. known
// reports whether the model's choice was inside the four-tier vocabulary;
// unknown vocabulary fails closed at Confirm without escalation.
func (j *JevJudge) ask(ctx context.Context, tool string, args json.RawMessage) (Level, float64, bool, bool) {
	body, err := json.Marshal(map[string]any{
		"model":     j.model(),
		"state":     j.jevState(tool, args),
		"questions": jevQuestion(),
	})
	if err != nil {
		return Confirm, 0, false, false
	}
	timeout := j.timeout()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, j.endpoint(), bytes.NewReader(body))
	if err != nil {
		return Confirm, 0, false, false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+j.key())
	resp, err := j.client().Do(req)
	if err != nil {
		return Confirm, 0, false, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Confirm, 0, false, false
	}
	var decoded struct {
		Answers map[string]struct {
			Choice     string  `json:"choice"`
			Confidence float64 `json:"confidence"`
		} `json:"answers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return Confirm, 0, false, false
	}
	a, ok := decoded.Answers["risk"]
	if !ok || strings.TrimSpace(a.Choice) == "" {
		return Confirm, 0, false, false
	}
	if !isKnownTier(a.Choice) {
		return Confirm, 0, false, true
	}
	return levelFromString(a.Choice), a.Confidence, true, true
}

func isKnownTier(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "allow", "confirm", "sandbox", "deny":
		return true
	default:
		return false
	}
}
