package extensions

import (
	"bytes"
	"context"
	"encoding/json"
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
	spec    extension.ToolSpec
	name    string
}

func (t *tool) ManagedExtension() {}
func (t *tool) Name() string      { return t.name }
func (t *tool) RequiresOperatorConfirmation() bool {
	s, err := t.manager.get(t.ref)
	if err != nil {
		return true
	}
	for _, pattern := range s.cfg.ConfirmTools {
		if match, _ := filepath.Match(pattern, t.spec.Name); match {
			return true
		}
	}
	return false
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
	if t.RequiresOperatorConfirmation() && !agentcore.OperatorConfirmedFor(ctx, id, t.name, args) {
		return fail("operation requires an exact operator confirmation; not executed")
	}
	s, e := m.get(t.ref)
	if e != nil {
		return fail(e.Error())
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
	s.mu.Lock()
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
		update(agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent(clean(p.Message, 1024))}})
	}
	r, e := s.conn.Call(ctx, extension.Call{ID: id, Tool: t.spec.Name, Arguments: args, OperatorConfirmed: agentcore.OperatorConfirmedFor(ctx, id, t.name, args)}, report)
	if e != nil {
		s.lastError = clean(e.Error(), 2048)
		s.state = "failed"
		return fail("extension call failed; execution outcome may be unknown: " + s.lastError)
	}
	return agentResult(r), nil // terminal result, never retry in the generic executor
}

func agentResult(r extension.Result) agentcore.AgentToolResult {
	out := agentcore.AgentToolResult{IsError: r.IsError}
	for _, b := range r.Content {
		switch b.Type {
		case "image":
			out.Content = append(out.Content, agentcore.NewImageContent(b.Data, b.MIMEType))
		case "resource_link":
			out.Content = append(out.Content, agentcore.NewTextContent(clean(b.Text+" "+b.URI, 4096)))
		default:
			out.Content = append(out.Content, agentcore.NewTextContent(clean(b.Text, outputLimit)))
		}
	}
	if len(r.StructuredContent) > 0 {
		out.Details = map[string]any{"structuredContent": r.StructuredContent}
		out.Content = append(out.Content, agentcore.NewTextContent(clean(string(r.StructuredContent), outputLimit)))
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
