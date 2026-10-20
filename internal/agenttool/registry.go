// This file implements the tool registry (US-014): tools are registered by
// name, and their arguments are validated against a per-tool JSON Schema
// (santhosh-tekuri/jsonschema v6) before execution. Validation failures are
// turned into a field-level error tool result rather than a Go error, so the
// model receives actionable feedback in the loop.
package agenttool

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/getan/golder/internal/agentcore"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// schemaPrinter renders jsonschema error kinds. LocalizedString dereferences
// the printer, so it must be non-nil.
var schemaPrinter = message.NewPrinter(language.English)

// ToolRegistry stores tools by name and validates call arguments against each
// tool's declared JSON Schema. It is safe for concurrent use.
type ToolRegistry struct {
	mu       sync.RWMutex
	tools    map[string]agentcore.AgentTool
	compiled map[string]*jsonschema.Schema
	shapes   map[string]objectShape
}

// NewToolRegistry returns an empty registry.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		tools:    make(map[string]agentcore.AgentTool),
		compiled: make(map[string]*jsonschema.Schema),
		shapes:   make(map[string]objectShape),
	}
}

// objectShape is the top-level object shape of a compiled tool schema. It is
// captured at registration so a validation error can name the fields the tool
// actually accepts, and so a call carrying a harmless foreign metadata field
// can be repaired without a round trip back to the model.
type objectShape struct {
	properties map[string]struct{}
	required   []string
	accepted   []string // properties, sorted for deterministic messages
	// closed reports that the schema explicitly sets additionalProperties:false,
	// which is the only case where dropping an unknown field is meaningful.
	closed bool
}

// Register adds a tool, compiling its JSON Schema up front so bad schemas fail
// at registration rather than on first call. A duplicate name is an error. A
// tool whose Schema() is empty is registered with no validation.
func (r *ToolRegistry) Register(tool agentcore.AgentTool) error {
	name := tool.Name()
	if name == "" {
		return fmt.Errorf("registry: tool has empty name")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[name]; exists {
		return fmt.Errorf("registry: tool %q already registered", name)
	}
	if raw := tool.Schema(); len(bytes.TrimSpace(raw)) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		sch, err := compileSchema(name, raw)
		if err != nil {
			return fmt.Errorf("registry: tool %q schema: %w", name, err)
		}
		r.compiled[name] = sch
		r.shapes[name] = shapeOf(sch)
	}
	r.tools[name] = tool
	return nil
}

// Get returns the tool registered under name and whether it was found.
func (r *ToolRegistry) Get(name string) (agentcore.AgentTool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// List returns all registered tools sorted by name (stable ordering for
// deterministic provider tool declarations).
func (r *ToolRegistry) List() []agentcore.AgentTool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]agentcore.AgentTool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// FieldError is a single validation failure located at a JSON-pointer path
// within the arguments.
type FieldError struct {
	Field   string `json:"field"`   // JSON pointer, e.g. "/path" or "" for root
	Message string `json:"message"` // human-readable reason
}

// Validate checks args against the tool's compiled schema. It returns nil when
// the tool has no schema or the arguments are valid; otherwise it returns the
// flattened field-level errors. An unknown tool name is reported as a single
// root-level error.
func (r *ToolRegistry) Validate(name string, args json.RawMessage) []FieldError {
	r.mu.RLock()
	_, known := r.tools[name]
	sch, hasSchema := r.compiled[name]
	r.mu.RUnlock()

	if !known {
		return []FieldError{{Field: "", Message: fmt.Sprintf("unknown tool %q", name)}}
	}
	if !hasSchema {
		return nil
	}

	var inst any
	dec := json.NewDecoder(bytes.NewReader(nonEmptyJSON(args)))
	dec.UseNumber()
	if err := dec.Decode(&inst); err != nil {
		return []FieldError{{Field: "", Message: fmt.Sprintf("arguments are not valid JSON: %v", err)}}
	}

	if err := sch.Validate(inst); err != nil {
		var verr *jsonschema.ValidationError
		if as := asValidationError(err); as != nil {
			verr = as
		}
		if verr != nil {
			shape := r.shapes[name]
			return flattenValidationError(verr, name, shape)
		}
		return []FieldError{{Field: "", Message: err.Error()}}
	}
	return nil
}

// RepairUnknownFields rewrites a call whose only schema failure is root-level
// additional properties. A field with a known alias is renamed (bash's
// "description" -> "justification"), a metadata-only field is dropped, and the
// rest of the call must then validate. Anything else is left alone so the model
// sees the validation error and fixes the call itself.
//
// The repair is deliberately narrow. It never drops an arbitrary unknown field:
// a field such as "cwd" could change what the command does, so silently
// ignoring it would be worse than one extra round trip. It also refuses when
// the remaining call is invalid (a missing required field is the model's to
// fix).
func (r *ToolRegistry) RepairUnknownFields(tool string, args json.RawMessage) (json.RawMessage, []string, bool) {
	r.mu.RLock()
	shape, hasShape := r.shapes[tool]
	r.mu.RUnlock()
	if !hasShape || !shape.closed || len(shape.properties) == 0 {
		return nil, nil, false
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(nonEmptyJSON(args), &obj); err != nil {
		return nil, nil, false
	}
	var unknown []string
	for name := range obj {
		if _, declared := shape.properties[name]; !declared {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) == 0 {
		return nil, nil, false
	}
	sort.Strings(unknown)

	notes := make([]string, 0, len(unknown))
	for _, name := range unknown {
		if target, ok := aliasField(tool, name); ok {
			if _, declared := shape.properties[target]; declared {
				if _, present := obj[target]; !present {
					obj[target] = obj[name]
					delete(obj, name)
					notes = append(notes, fmt.Sprintf("mapped unknown field %q to %q", name, target))
					continue
				}
			}
			delete(obj, name)
			notes = append(notes, fmt.Sprintf("ignored unknown field %q", name))
			continue
		}
		if repairableMetadataFields[name] {
			delete(obj, name)
			notes = append(notes, fmt.Sprintf("ignored unknown field %q", name))
			continue
		}
		return nil, nil, false
	}

	repaired, err := json.Marshal(obj)
	if err != nil {
		return nil, nil, false
	}
	if errs := r.Validate(tool, repaired); len(errs) > 0 {
		return nil, nil, false
	}
	return repaired, notes, true
}

// repairableMetadataFields is the closed set of harness-metadata fields that
// carry no execution semantics. RepairUnknownFields only ever drops these.
var repairableMetadataFields = map[string]bool{
	"description": true,
	"note":        true,
	"comment":     true,
}

// unknownFieldHints maps a field name another agent harness uses to the field
// this tool set uses for the same purpose. Keep it tiny: this covers a
// convention a model is likely to carry over, not every synonym.
var unknownFieldHints = map[string]map[string]string{
	"bash": {"description": "justification"},
}

func aliasField(tool, field string) (string, bool) {
	hints, ok := unknownFieldHints[tool]
	if !ok {
		return "", false
	}
	target, ok := hints[field]
	return target, ok
}

// ValidationErrorResult builds an error AgentToolResult describing the given
// field errors, for the loop to hand back to the model (FR: field-level error
// tool result). Terminate is left nil (a validation failure never ends the run).
func ValidationErrorResult(toolName string, errs []FieldError) agentcore.AgentToolResult {
	var b strings.Builder
	fmt.Fprintf(&b, "Invalid arguments for tool %q:\n", toolName)
	for _, e := range errs {
		field := e.Field
		if field == "" {
			field = "(root)"
		}
		fmt.Fprintf(&b, "  - %s: %s\n", field, e.Message)
	}
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(strings.TrimRight(b.String(), "\n"))},
		Details: errs,
	}
}

// compileSchema compiles a raw JSON Schema document held in memory.
func compileSchema(name string, raw json.RawMessage) (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	// A synthetic in-memory URL; each tool gets its own so schemas never clash.
	loc := "mem:///" + name + ".json"
	if err := c.AddResource(loc, doc); err != nil {
		return nil, err
	}
	return c.Compile(loc)
}

// shapeOf captures the top-level object shape of a compiled schema. A root
// $ref is followed a bounded number of times so a schema written as a
// reference still yields its fields.
func shapeOf(sch *jsonschema.Schema) objectShape {
	shape := objectShape{}
	for hops := 0; sch != nil && sch.Ref != nil && hops < 8; hops++ {
		sch = sch.Ref
	}
	if sch == nil {
		return shape
	}
	if allowed, ok := sch.AdditionalProperties.(bool); ok && !allowed {
		shape.closed = true
	}
	shape.properties = make(map[string]struct{}, len(sch.Properties))
	shape.accepted = make([]string, 0, len(sch.Properties))
	for name := range sch.Properties {
		shape.properties[name] = struct{}{}
		shape.accepted = append(shape.accepted, name)
	}
	sort.Strings(shape.accepted)
	shape.required = append([]string(nil), sch.Required...)
	return shape
}

// flattenValidationError walks the ValidationError tree and returns one
// FieldError per leaf cause (the most specific failures), falling back to the
// node itself when it has no causes. additionalProperties failures are
// reported per offending property, at /<name> rather than at (root), so the
// model can see which field to drop and which fields the tool accepts.
func flattenValidationError(e *jsonschema.ValidationError, tool string, shape objectShape) []FieldError {
	var out []FieldError
	var walk func(n *jsonschema.ValidationError)
	walk = func(n *jsonschema.ValidationError) {
		if ap, ok := n.ErrorKind.(*kind.AdditionalProperties); ok && len(ap.Properties) > 0 {
			props := append([]string(nil), ap.Properties...)
			sort.Strings(props)
			for _, prop := range props {
				loc := append(append([]string(nil), n.InstanceLocation...), prop)
				out = append(out, FieldError{
					Field:   jsonPointer(loc),
					Message: unknownFieldMessage(tool, prop, n.InstanceLocation, shape),
				})
			}
			return
		}
		if len(n.Causes) == 0 {
			out = append(out, FieldError{
				Field:   jsonPointer(n.InstanceLocation),
				Message: n.ErrorKind.LocalizedString(schemaPrinter),
			})
			return
		}
		for _, c := range n.Causes {
			walk(c)
		}
	}
	walk(e)
	if len(out) == 0 {
		out = append(out, FieldError{Field: jsonPointer(e.InstanceLocation), Message: e.Error()})
	}
	return out
}

// unknownFieldMessage explains an additionalProperties failure. At the root it
// names every accepted field and, when the name looks like a carried-over
// convention or a typo, the field the caller probably meant. Nested objects do
// not get the root's field list, which would be misleading there.
func unknownFieldMessage(tool, field string, at []string, shape objectShape) string {
	if len(at) > 0 {
		return fmt.Sprintf("unknown field %q is not allowed in this object", field)
	}
	msg := fmt.Sprintf("unknown field %q", field)
	if len(shape.accepted) > 0 {
		msg += "; accepted: " + strings.Join(shape.accepted, ", ")
	}
	if hint := suggestField(tool, field, shape); hint != "" {
		msg += fmt.Sprintf("; did you mean %q?", hint)
	}
	return msg
}

// suggestField returns the accepted field the caller most likely meant:
// a known cross-harness alias first, then the closest spelling within two
// edits. Empty means "no confident suggestion".
func suggestField(tool, field string, shape objectShape) string {
	if target, ok := aliasField(tool, field); ok {
		if _, declared := shape.properties[target]; declared {
			return target
		}
	}
	lower := strings.ToLower(field)
	best, bestDist := "", 3
	for name := range shape.properties {
		d := editDistance(lower, strings.ToLower(name))
		if d < bestDist || (d == bestDist && name < best) {
			best, bestDist = name, d
		}
	}
	return best
}

// editDistance is the Levenshtein distance between two short strings.
func editDistance(a, b string) int {
	ar, br := []rune(a), []rune(b)
	if len(ar) == 0 {
		return len(br)
	}
	prev := make([]int, len(br)+1)
	cur := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		cur[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			cur[j] = min(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(br)]
}

// jsonPointer renders an instance-location path as a JSON pointer.
func jsonPointer(loc []string) string {
	if len(loc) == 0 {
		return ""
	}
	var b strings.Builder
	for _, tok := range loc {
		b.WriteByte('/')
		tok = strings.ReplaceAll(tok, "~", "~0")
		tok = strings.ReplaceAll(tok, "/", "~1")
		b.WriteString(tok)
	}
	return b.String()
}

// asValidationError extracts a *jsonschema.ValidationError from err if present.
func asValidationError(err error) *jsonschema.ValidationError {
	if verr, ok := err.(*jsonschema.ValidationError); ok {
		return verr
	}
	return nil
}

// nonEmptyJSON treats empty arguments as an empty object so schemas with only
// optional properties validate, and "required" violations are reported.
func nonEmptyJSON(args json.RawMessage) []byte {
	if len(bytes.TrimSpace(args)) == 0 {
		return []byte("{}")
	}
	return args
}
