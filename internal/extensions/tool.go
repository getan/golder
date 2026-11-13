package extensions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/getan/golder/extension"
	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/permissions"
)

type tool struct {
	manager *Manager
	ref     SourceRef
	origin  *source
	spec    extension.ToolSpec
	name    string
}

func (t *tool) ManagedExtension() {}
func (t *tool) Name() string      { return t.name }

// approvalFor reads one immutable source definition. Lark's known writes and
// all configured confirmation tools use host policy; other sources retain
// their existing human-only confirmation contract.
func (t *tool) approvalFor(s *source) (operator, policy bool) {
	isLark := t.isLarkSource(s)
	if isLark && t.larkBuiltinWrite() {
		return false, true
	}
	for _, pattern := range s.cfg.ConfirmTools {
		if match, _ := filepath.Match(pattern, t.spec.Name); match {
			if isLark {
				return false, true
			}
			return true, false
		}
	}
	return false, false
}

func (t *tool) isLarkSource(s *source) bool {
	if t.ref.Kind != "mcp" || s == nil {
		return false
	}
	return t.ref.ID == "lark" || (s.cfg.Transport == "stdio" && filepath.Base(s.cfg.Command) == executableName("golder-lark"))
}

func (t *tool) larkBuiltinWrite() bool {
	switch t.spec.Name {
	case "doc_write", "drive_write", "message_send", "ticket_update", "ticket_transition", "sheet_write", "sheet_structure", "base_record_write", "calendar_write", "task_write", "wiki_write", "file_transfer", "cli_call", "cli_raw":
		return true
	default:
		return false
	}
}

func (t *tool) approvalRequirements() (operator, policy bool) {
	s, err := t.manager.get(t.ref)
	if err != nil {
		return true, false
	}
	return t.approvalFor(s)
}

func (t *tool) RequiresOperatorConfirmation() bool {
	operator, _ := t.approvalRequirements()
	return operator
}
func (t *tool) RequiresPolicyApproval() bool {
	_, policy := t.approvalRequirements()
	return policy
}

func (t *tool) Description() string {
	return fmt.Sprintf("[%s / %s; external, hints are not authorization] %s", t.ref, t.spec.Name, t.spec.Description)
}
func (t *tool) Schema() json.RawMessage                    { return append(json.RawMessage{}, t.spec.InputSchema...) }
func (t *tool) ExecutionMode() agentcore.ToolExecutionMode { return agentcore.ToolExecutionSequential }
func (t *tool) Execute(ctx context.Context, id string, args json.RawMessage, update agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	fail := func(msg string) (agentcore.AgentToolResult, error) {
		return agentcore.AgentToolResult{IsError: true, Content: agentcore.ContentList{agentcore.NewTextContent(clean(msg, 8192))}}, nil
	}
	m := t.manager
	if !agentcore.ExternalToolAuthorized(ctx) {
		return fail("external tool requires the host authorization chain; not executed")
	}
	s, e := m.get(t.ref)
	if e != nil {
		return fail(e.Error())
	}
	fail = func(msg string) (agentcore.AgentToolResult, error) {
		return agentcore.AgentToolResult{IsError: true, Content: agentcore.ContentList{agentcore.NewTextContent(s.logs.Clean(msg, 8192))}}, nil
	}
	if s != t.origin && (t.isLarkSource(t.origin) || t.isLarkSource(s)) {
		return fail("Lark source changed since this tool was registered; refresh the catalog before executing")
	}
	requireOperator, requirePolicy := t.approvalFor(s)
	if requireOperator && !agentcore.OperatorConfirmedFor(ctx, id, t.name, args) {
		return fail("operation requires an exact operator confirmation; not executed")
	}
	if requirePolicy && !agentcore.PolicyApprovedFor(ctx, id, t.name, args) {
		return fail("operation requires exact-call authorization by the host policy; not executed")
	}
	if !s.enabled.Load() || m.closed.Load() {
		return fail("extension disabled; not executed")
	}
	full := m.opts.Permissions != nil && m.opts.Permissions.Mode() == permissions.FullAccess
	if (s.cfg.Network || s.cfg.Transport == "http") && !full && !agentcore.NetworkGrantedFromContext(ctx) {
		return fail("external tool needs an explicit network grant; not executed")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout(s.cfg.CallTimeout, 120*time.Second))
	defer cancel()
	if callScopedLarkState(s.cfg) {
		if err := lockLarkSource(ctx, s); err != nil {
			return fail("Lark operation was not dispatched: " + err.Error())
		}
	} else {
		s.mu.Lock()
	}
	defer s.mu.Unlock()
	defer m.publish(s)
	if !s.enabled.Load() {
		return fail("extension disabled; not executed")
	}
	if e = m.connectLocked(ctx, s); e != nil {
		return fail(e.Error())
	}
	stop := context.AfterFunc(s.life, cancel)
	defer stop()
	// Refuse stale schemas, including changes discovered while lazily connecting.
	var current *extension.ToolSpec
	for i := range s.catalog.Tools {
		if s.catalog.Tools[i].Name == t.spec.Name {
			current = &s.catalog.Tools[i]
			break
		}
	}
	if current == nil || !sameSchema(current.InputSchema, t.spec.InputSchema) {
		return fail("extension tool definition changed; not executed; refresh the catalog before retrying")
	}
	if !m.toolAllowed(s, t.spec.Name) {
		return fail("extension tool blocked by policy; not executed")
	}
	sch, e := compileSchema(t.spec.InputSchema)
	if e != nil {
		return fail("invalid input schema; not executed")
	}
	var input any
	if json.Unmarshal(args, &input) != nil || sch.Validate(input) != nil {
		return fail("invalid arguments; not executed")
	}
	last := time.Time{}
	report := func(p extension.Progress) {
		if update == nil || time.Since(last) < 100*time.Millisecond {
			return
		}
		last = time.Now()
		update(agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent(s.logs.Clean(p.Message, 1024))}})
	}
	conn := s.conn
	_, callScoped := conn.(*larkStateBackend)
	var r extension.Result
	func() {
		if callScoped {
			// Ownership/schema admission is complete. Restore the mutex even on
			// panic; the outer snapshot publication still requires source.mu.
			s.mu.Unlock()
			defer s.mu.Lock()
		}
		r, e = conn.Call(ctx, extension.Call{ID: id, Tool: t.spec.Name, Arguments: args,
			OperatorConfirmed: agentcore.OperatorConfirmedFor(ctx, id, t.name, args),
			PolicyApproved:    requirePolicy && agentcore.PolicyApprovedFor(ctx, id, t.name, args)}, report)
	}()
	if e != nil {
		var notDispatched *larkNotDispatchedError
		if errors.As(e, &notDispatched) {
			return fail(e.Error()) // queued cancellation/schema changes are not unknown writes
		}
		message := s.logs.Clean(e.Error(), 2048)
		if s.conn == conn && s.enabled.Load() {
			s.lastError, s.state = message, "failed"
		}
		return fail("extension call failed; execution outcome may be unknown: " + message)
	}
	if callScoped && s.conn == conn && s.enabled.Load() {
		s.state, s.lastError = "on demand", ""
	}
	return agentResultForSource(r, &s.logs), nil // terminal result, never retry in the generic executor
}

func agentResult(r extension.Result) agentcore.AgentToolResult {
	return agentResultForSource(r, nil)
}

func agentResultForSource(r extension.Result, logs *logBuffer) agentcore.AgentToolResult {
	filter := clean
	if logs != nil {
		filter = logs.Clean
	}
	out := agentcore.AgentToolResult{IsError: r.IsError}
	for _, b := range r.Content {
		switch b.Type {
		case "image":
			out.Content = append(out.Content, agentcore.NewImageContent(b.Data, b.MIMEType))
		case "resource_link":
			out.Content = append(out.Content, agentcore.NewTextContent(filter(b.Text+" "+b.URI, 4096)))
		default:
			out.Content = append(out.Content, agentcore.NewTextContent(filter(b.Text, outputLimit)))
		}
	}
	if len(r.StructuredContent) > 0 {
		if logs != nil && logs.ContainsSecretJSON(r.StructuredContent) {
			out.Content = append(out.Content, agentcore.NewTextContent("[structured content withheld: contains a credential]"))
		} else {
			out.Details = map[string]any{"structuredContent": r.StructuredContent}
			out.Content = append(out.Content, agentcore.NewTextContent(filter(string(r.StructuredContent), outputLimit)))
		}
	}
	if len(out.Content) == 0 {
		out.Content = agentcore.ContentList{agentcore.NewTextContent("(no content)")}
	}
	return out
}

func sameSchema(a, b json.RawMessage) bool {
	var x, y bytes.Buffer
	return json.Compact(&x, a) == nil && json.Compact(&y, b) == nil && bytes.Equal(x.Bytes(), y.Bytes())
}
