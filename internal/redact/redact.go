package redact

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/getan/golder/internal/agentcore"
)

// Mode selects what a finding does to the text.
type Mode int

const (
	// Off disables scanning entirely (Default returns nil in this mode).
	Off Mode = iota
	// Observe records findings and leaves the text untouched.
	Observe
	// Redact replaces each secret with a stable placeholder (the default).
	Redact
	// Block replaces the whole text with a short refusal that names the rules.
	Block
)

func (m Mode) String() string {
	switch m {
	case Observe:
		return "observe"
	case Redact:
		return "redact"
	case Block:
		return "block"
	default:
		return "off"
	}
}

// ParseMode parses a user-facing mode name. An empty string means Redact, the
// safe default for a configured redactor.
func ParseMode(s string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "redact":
		return Redact, nil
	case "off":
		return Off, nil
	case "observe":
		return Observe, nil
	case "block":
		return Block, nil
	default:
		return Off, fmt.Errorf("redact: unknown mode %q (want off, observe, redact or block)", s)
	}
}

// Seam names the point a scan happened at, for the audit log.
type Seam string

const (
	SeamToolOutput Seam = "tool_output"
	SeamUserPrompt Seam = "user_prompt"
	SeamAssistant  Seam = "assistant"
	SeamCompaction Seam = "compaction"
	SeamMemory     Seam = "memory"
	SeamSession    Seam = "session"
	SeamShare      Seam = "share"
)

// Config configures one Redactor.
type Config struct {
	Mode      Mode
	Entropy   float64 // minimum Shannon entropy for generic assignments; 0 = 3.5
	Allow     []string
	AuditPath string
	SessionID string
	// Placeholder overrides the token format. The share export uses it to emit
	// a plain [REDACTED] instead of a reversible token.
	Placeholder func(rule, hash string) string
	MaxTokens   int // restore-map cap; 0 = 4096
	// Aggressive disables the placeholder/entropy heuristics. The share export
	// uses it: a published transcript must redact "xxxx" and low-entropy
	// assignments too, even though they are usually benign in a live tool result.
	Aggressive bool
	// WholeAssignments replaces the whole NAME=value match for the generic
	// assignment rule instead of only the value. Export-only.
	WholeAssignments bool
}

// Finding is one detected credential. Hash is a keyed digest prefix; the
// secret itself is deliberately absent.
type Finding struct {
	Rule   string
	Action string
	Hash   string
	Start  int
	End    int
	Line   int
	Col    int
}

// Result is one scan's outcome.
type Result struct {
	Text     string
	Findings []Finding
	Blocked  bool
}

// Redactor is immutable after construction except for the restore map and the
// known-secret set, both of which are mutex-guarded. Scan is safe to call from
// concurrent tool executions.
type Redactor struct {
	mode        Mode
	rules       []*rule
	aho         *automaton
	allow       map[string]struct{}
	placeholder func(rule, hash string) string
	audit       *auditor
	session     string
	salt        [16]byte
	aggressive  bool
	wholeAssign bool

	mu        sync.RWMutex
	tokens    map[string]string
	known     map[string]struct{}
	maxTokens int
}

// New builds a redactor. The rule table and automaton are built once; a
// process should normally create one and reuse it.
func New(cfg Config) (*Redactor, error) {
	if cfg.Entropy <= 0 {
		cfg.Entropy = 3.5
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 4096
	}

	r := &Redactor{
		mode:        cfg.Mode,
		allow:       make(map[string]struct{}, len(cfg.Allow)),
		placeholder: cfg.Placeholder,
		session:     cfg.SessionID,
		tokens:      make(map[string]string),
		known:       make(map[string]struct{}),
		maxTokens:   cfg.MaxTokens,
		aggressive:  cfg.Aggressive,
		wholeAssign: cfg.WholeAssignments,
	}
	if _, err := rand.Read(r.salt[:]); err != nil {
		return nil, fmt.Errorf("redact: salt: %w", err)
	}
	for _, v := range cfg.Allow {
		if v != "" {
			r.allow[v] = struct{}{}
		}
	}
	// Clone the table so a per-instance entropy threshold never races another
	// redactor's scan.
	r.rules = make([]*rule, len(builtinRules))
	for i, src := range builtinRules {
		cp := *src
		if cp.id == "generic_assignment" {
			cp.entropy = cfg.Entropy
		}
		r.rules[i] = &cp
	}
	r.aho = buildAho(r.rules)
	r.audit = newAuditor(cfg.AuditPath)
	return r, nil
}

// AddKnown registers exact secret values (for example golder's own provider
// API keys) that must always be redacted even when their shape is unknown.
func (r *Redactor) AddKnown(values ...string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, v := range values {
		if len(v) >= 6 {
			r.known[v] = struct{}{}
		}
	}
}

// Scan finds secrets in text and applies the configured mode.
func (r *Redactor) Scan(seam Seam, tool, text string) Result {
	if r == nil || r.mode == Off || text == "" {
		return Result{Text: text}
	}
	spans := selectSpans(r.find(text))
	if len(spans) == 0 {
		return Result{Text: text}
	}

	action := r.mode.String()
	findings := make([]Finding, 0, len(spans))
	var names []string
	seenNames := make(map[string]struct{})

	var b strings.Builder
	if r.mode == Redact {
		b.Grow(len(text))
	}
	last := 0
	for _, s := range spans {
		secret := text[s.start:s.end]
		h := r.hashValue(secret)
		line, col := lineCol(text, s.start)
		findings = append(findings, Finding{
			Rule: s.rule, Action: action, Hash: h,
			Start: s.start, End: s.end, Line: line, Col: col,
		})
		if _, ok := seenNames[s.rule]; !ok {
			seenNames[s.rule] = struct{}{}
			names = append(names, s.rule)
		}
		if r.mode == Redact {
			b.WriteString(text[last:s.start])
			b.WriteString(r.token(s.rule, secret))
			last = s.end
		}
	}

	for _, f := range findings {
		r.audit.log(auditEntry{
			Time:    time.Now().UTC().Format(time.RFC3339),
			Session: r.session, Seam: string(seam), Tool: tool,
			Rule: f.Rule, Action: f.Action, Hash: f.Hash, Line: f.Line, Col: f.Col,
		})
	}

	switch r.mode {
	case Observe:
		return Result{Text: text, Findings: findings}
	case Block:
		sort.Strings(names)
		return Result{
			Text:     "[blocked by redact: " + strings.Join(names, ", ") + "]",
			Findings: findings,
			Blocked:  true,
		}
	default:
		b.WriteString(text[last:])
		return Result{Text: b.String(), Findings: findings}
	}
}

// Text is Scan's text-only convenience form.
func (r *Redactor) Text(seam Seam, tool, text string) string {
	return r.Scan(seam, tool, text).Text
}

// Messages redacts every message in the list. It returns a new slice only while
// the redactor is active; individual messages are copied only when they change.
func (r *Redactor) Messages(seam Seam, msgs agentcore.MessageList) agentcore.MessageList {
	if r == nil || r.mode == Off || len(msgs) == 0 {
		return msgs
	}
	out := make(agentcore.MessageList, len(msgs))
	for i, m := range msgs {
		out[i] = r.Message(seam, m)
	}
	return out
}

// Message redacts the text-bearing fields of one message. Tool-call arguments
// and thinking signatures are left alone: rewriting them would break replay or
// execution.
func (r *Redactor) Message(seam Seam, m agentcore.Message) agentcore.Message {
	if r == nil || r.mode == Off {
		return m
	}
	switch msg := m.(type) {
	case agentcore.UserMessage:
		if content, changed := r.content(seam, "", msg.Content); changed {
			msg.Content = content
			return msg
		}
	case agentcore.AssistantMessage:
		content, changed := r.content(seam, "", msg.Content)
		text := msg.ErrorMessage
		if text != "" {
			text = r.Text(seam, "", text)
		}
		if changed || text != msg.ErrorMessage {
			msg.Content = content
			msg.ErrorMessage = text
			return msg
		}
	case agentcore.ToolResultMessage:
		if content, changed := r.content(SeamToolOutput, msg.ToolName, msg.Content); changed {
			msg.Content = content
			return msg
		}
	case agentcore.CompactionMessage:
		if text := r.Text(SeamCompaction, "", msg.Summary); text != msg.Summary {
			msg.Summary = text
			return msg
		}
	}
	return m
}

// ToolResult redacts a tool result before it enters the message list. In block
// mode the whole result is replaced by a refusal and marked as an error.
func (r *Redactor) ToolResult(tool string, res agentcore.AgentToolResult) agentcore.AgentToolResult {
	if r == nil || r.mode == Off {
		return res
	}
	var out agentcore.ContentList
	changed, blocked := false, false
	blockText := ""
	for i, c := range res.Content {
		t, ok := c.(agentcore.TextContent)
		if !ok {
			continue
		}
		s := r.Scan(SeamToolOutput, tool, t.Text)
		if s.Blocked {
			blocked = true
			blockText = s.Text
		}
		if s.Text == t.Text {
			continue
		}
		if !changed {
			out = append(agentcore.ContentList(nil), res.Content...)
			changed = true
		}
		out[i] = agentcore.TextContent{Type: t.Type, Text: s.Text, TextSignature: t.TextSignature}
	}
	if blocked {
		res.Content = agentcore.ContentList{agentcore.NewTextContent(blockText)}
		res.IsError = true
		return res
	}
	if changed {
		res.Content = out
	}
	return res
}

// Restore expands known placeholders back to their values. Call it only on
// write-tool arguments: restoring into a shell command would let the model use
// a placeholder as an oracle to exfiltrate the secret.
func (r *Redactor) Restore(s string) (string, int) {
	return r.restore(s, false)
}

// RestoreJSON is Restore for raw tool arguments. The replacement is
// JSON-escaped, so a secret containing quotes, backslashes or newlines (a PEM
// block) cannot break the argument document.
func (r *Redactor) RestoreJSON(s string) (string, int) {
	return r.restore(s, true)
}

func (r *Redactor) restore(s string, jsonEscape bool) (string, int) {
	if r == nil || s == "" {
		return s, 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.tokens) == 0 {
		return s, 0
	}
	n := 0
	for token, secret := range r.tokens {
		if strings.Contains(s, token) {
			if jsonEscape {
				secret = escapeJSONString(secret)
			}
			n += strings.Count(s, token)
			s = strings.ReplaceAll(s, token, secret)
		}
	}
	return s, n
}

func escapeJSONString(s string) string {
	b, err := json.Marshal(s)
	if err != nil || len(b) < 2 {
		return s
	}
	return string(b[1 : len(b)-1])
}

// content redacts text blocks, copying the list on the first change.
func (r *Redactor) content(seam Seam, tool string, cl agentcore.ContentList) (agentcore.ContentList, bool) {
	changed := false
	var out agentcore.ContentList
	for i, c := range cl {
		t, ok := c.(agentcore.TextContent)
		if !ok {
			continue
		}
		s := r.Scan(seam, tool, t.Text)
		if s.Text == t.Text {
			continue
		}
		if !changed {
			out = append(agentcore.ContentList(nil), cl...)
			changed = true
		}
		out[i] = agentcore.TextContent{Type: t.Type, Text: s.Text, TextSignature: t.TextSignature}
	}
	if !changed {
		return cl, false
	}
	return out, true
}

type span struct {
	start, end int
	priority   int
	rule       string
}

func (r *Redactor) find(text string) []span {
	var out []span
	seen := make(map[[3]string]struct{})
	add := func(s span) {
		key := [3]string{fmt.Sprint(s.start), fmt.Sprint(s.end), s.rule}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, s)
	}

	// Known values are exact matches: this catches a credential whose shape the
	// rule table does not know, including golder's own provider keys.
	r.mu.RLock()
	for value := range r.known {
		for from := 0; from < len(text); {
			i := strings.Index(text[from:], value)
			if i < 0 {
				break
			}
			start := from + i
			add(span{start: start, end: start + len(value), priority: 0, rule: "known_secret"})
			from = start + len(value)
		}
	}
	r.mu.RUnlock()

	if r.aho == nil {
		return out
	}
	state := 0
	wholeDone := make([]bool, len(r.rules))
	for i := 0; i < len(text); i++ {
		state = r.aho.step(state, text[i])
		if len(r.aho.nodes[state].out) == 0 {
			continue
		}
		for _, ri := range r.aho.nodes[state].out {
			rule := r.rules[ri]
			if rule.wholeText {
				if wholeDone[ri] {
					continue
				}
				wholeDone[ri] = true
				for _, s := range r.matches(rule, text, 0) {
					add(s)
				}
				continue
			}
			if rule.requiresAssign && !nearAssign(text, i+1) {
				continue
			}
			start := i + 1 - 192
			if start < 0 {
				start = 0
			}
			end := i + 1 + 640
			if end > len(text) {
				end = len(text)
			}
			for _, s := range r.matches(rule, text[start:end], start) {
				add(s)
			}
		}
	}
	return out
}

func (r *Redactor) matches(rule *rule, text string, base int) []span {
	var out []span
	for _, m := range rule.pattern.FindAllStringSubmatchIndex(text, -1) {
		group := rule.group
		if r.wholeAssign && rule.id == "generic_assignment" {
			group = 0
		}
		gi := group * 2
		if gi+1 >= len(m) || m[gi] < 0 || m[gi+1] < 0 {
			continue
		}
		start, end := m[gi], m[gi+1]
		if group > 0 {
			start, end = trimCapture(text, start, end)
		}
		if end <= start {
			continue
		}
		if !r.accept(rule, text[start:end]) {
			continue
		}
		out = append(out, span{start: base + start, end: base + end, priority: rule.priority, rule: rule.id})
	}
	return out
}

func (r *Redactor) accept(rule *rule, value string) bool {
	min := rule.minLen
	if min == 0 {
		min = 8
	}
	if len(value) < min || (rule.maxLen > 0 && len(value) > rule.maxLen) {
		return false
	}
	if strings.Contains(value, "«SECRET:") {
		return false
	}
	if _, ok := r.allow[value]; ok {
		return false
	}
	if !r.aggressive {
		if placeholderLike(value) {
			return false
		}
		if rule.entropy > 0 && shannon(value) < rule.entropy {
			return false
		}
	}
	return true
}

func selectSpans(spans []span) []span {
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].priority != spans[j].priority {
			return spans[i].priority < spans[j].priority
		}
		if spans[i].start != spans[j].start {
			return spans[i].start < spans[j].start
		}
		return spans[i].end-spans[i].start > spans[j].end-spans[j].start
	})
	chosen := make([]span, 0, len(spans))
	for _, s := range spans {
		overlap := false
		for _, c := range chosen {
			if s.start < c.end && c.start < s.end {
				overlap = true
				break
			}
		}
		if !overlap {
			chosen = append(chosen, s)
		}
	}
	sort.Slice(chosen, func(i, j int) bool { return chosen[i].start < chosen[j].start })
	return chosen
}

func (r *Redactor) token(rule, secret string) string {
	h := r.hashValue(secret)
	if r.placeholder != nil {
		return r.placeholder(rule, h)
	}
	tok := "«SECRET:" + rule + ":" + h + "»"
	r.mu.Lock()
	if _, ok := r.tokens[tok]; !ok && len(r.tokens) < r.maxTokens {
		r.tokens[tok] = secret
	}
	r.mu.Unlock()
	return tok
}

func (r *Redactor) hashValue(value string) string {
	mac := hmac.New(sha256.New, r.salt[:])
	mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil)[:4])
}

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func placeholderLike(value string) bool {
	low := strings.ToLower(value)
	for _, marker := range []string{"example", "placeholder", "changeme", "redacted", "your_", "your-", "xxxx", "****"} {
		if strings.Contains(low, marker) {
			return true
		}
	}
	switch low {
	case "true", "false", "null", "none", "nil":
		return true
	}
	if strings.HasPrefix(value, "$") || strings.Contains(value, "${") || strings.Contains(value, "<") {
		return true
	}
	if len(value) == 40 || len(value) == 64 {
		if isHex(low) {
			return true
		}
	}
	if uuidPattern.MatchString(low) {
		return true
	}
	allSame := true
	for i := 1; i < len(value); i++ {
		if value[i] != value[0] {
			allSame = false
			break
		}
	}
	return allSame
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return len(s) > 0
}

func nearAssign(text string, from int) bool {
	end := from + 48
	if end > len(text) {
		end = len(text)
	}
	for i := from; i < end; i++ {
		switch text[i] {
		case '=', ':':
			return true
		case '\n':
			if i > from {
				return false
			}
		}
	}
	return false
}

func trimCapture(text string, start, end int) (int, int) {
	for end > start {
		switch text[end-1] {
		case ',', ';', ')', ']', '}', '"', '\'':
			end--
		default:
			return start, end
		}
	}
	return start, end
}

func lineCol(text string, off int) (int, int) {
	if off > len(text) {
		off = len(text)
	}
	line := 1 + strings.Count(text[:off], "\n")
	col := off - strings.LastIndex(text[:off], "\n")
	return line, col
}

// Default is the process-wide redactor. Production enables it from config;
// tests and embedders see nil (no scanning) unless they call SetDefault.
var defaultRedactor atomic.Pointer[Redactor]

func Default() *Redactor     { return defaultRedactor.Load() }
func SetDefault(r *Redactor) { defaultRedactor.Store(r) }

// Configure builds and installs the process redactor. Mode Off clears it.
func Configure(cfg Config) error {
	if cfg.Mode == Off {
		SetDefault(nil)
		return nil
	}
	r, err := New(cfg)
	if err != nil {
		return err
	}
	SetDefault(r)
	return nil
}

// ShareText redacts a transcript for publication, using a plain marker rather
// than a reversible token.
func ShareText(s string) string {
	shareOnce.Do(func() {
		shareRedactor, _ = New(Config{
			Mode:             Redact,
			Aggressive:       true,
			WholeAssignments: true,
			Placeholder: func(_, _ string) string {
				return "[REDACTED]"
			},
			MaxTokens: 1,
		})
	})
	if shareRedactor == nil {
		return s
	}
	return shareRedactor.Text(SeamShare, "", s)
}

var (
	shareOnce     sync.Once
	shareRedactor *Redactor
)
