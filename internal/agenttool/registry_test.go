package agenttool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

// stubTool is a minimal AgentTool for registry tests.
type stubTool struct {
	name   string
	schema string
}

func (s stubTool) Name() string                               { return s.name }
func (s stubTool) Description() string                        { return "stub" }
func (s stubTool) Schema() json.RawMessage                    { return json.RawMessage(s.schema) }
func (s stubTool) ExecutionMode() agentcore.ToolExecutionMode { return agentcore.ToolExecutionParallel }
func (s stubTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent("ok")}}, nil
}

const personSchema = `{
  "type": "object",
  "properties": {
    "name": {"type": "string"},
    "age":  {"type": "integer"}
  },
  "required": ["name"],
  "additionalProperties": false
}`

func newTestRegistry(t *testing.T) *ToolRegistry {
	t.Helper()
	r := NewToolRegistry()
	if err := r.Register(stubTool{name: "person", schema: personSchema}); err != nil {
		t.Fatalf("register: %v", err)
	}
	return r
}

func TestRegistryRegisterAndGet(t *testing.T) {
	r := newTestRegistry(t)
	tool, ok := r.Get("person")
	if !ok || tool.Name() != "person" {
		t.Fatalf("Get(person) failed: %v %v", tool, ok)
	}
	if _, ok := r.Get("missing"); ok {
		t.Error("Get(missing) should report not found")
	}
	if got := r.List(); len(got) != 1 || got[0].Name() != "person" {
		t.Errorf("List wrong: %+v", got)
	}
}

func TestRegistryDuplicateRejected(t *testing.T) {
	r := newTestRegistry(t)
	if err := r.Register(stubTool{name: "person", schema: personSchema}); err == nil {
		t.Fatal("expected duplicate registration to error")
	}
}

func TestRegistryValidArgs(t *testing.T) {
	r := newTestRegistry(t)
	errs := r.Validate("person", json.RawMessage(`{"name":"ada","age":36}`))
	if errs != nil {
		t.Fatalf("valid args reported errors: %+v", errs)
	}
}

func TestRegistryMissingRequiredField(t *testing.T) {
	r := newTestRegistry(t)
	errs := r.Validate("person", json.RawMessage(`{"age":36}`))
	if len(errs) == 0 {
		t.Fatal("expected error for missing required field 'name'")
	}
}

func TestRegistryTypeError(t *testing.T) {
	r := newTestRegistry(t)
	errs := r.Validate("person", json.RawMessage(`{"name":"ada","age":"old"}`))
	if len(errs) == 0 {
		t.Fatal("expected type error for age")
	}
	// The offending field should be located at /age.
	found := false
	for _, e := range errs {
		if e.Field == "/age" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a field error at /age, got %+v", errs)
	}
}

func TestRegistryUnknownTool(t *testing.T) {
	r := newTestRegistry(t)
	errs := r.Validate("nope", json.RawMessage(`{}`))
	if len(errs) != 1 || errs[0].Field != "" {
		t.Fatalf("expected single root error for unknown tool, got %+v", errs)
	}
}

func TestRegistryNoSchemaSkipsValidation(t *testing.T) {
	r := NewToolRegistry()
	if err := r.Register(stubTool{name: "free", schema: ""}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if errs := r.Validate("free", json.RawMessage(`{"anything":true}`)); errs != nil {
		t.Fatalf("no-schema tool should skip validation, got %+v", errs)
	}
}

func TestValidationErrorResultShape(t *testing.T) {
	r := newTestRegistry(t)
	errs := r.Validate("person", json.RawMessage(`{"age":36}`))
	res := ValidationErrorResult("person", errs)
	if len(res.Content) == 0 {
		t.Fatal("expected content in validation error result")
	}
	if _, ok := res.Details.([]FieldError); !ok {
		t.Errorf("expected Details to carry []FieldError, got %T", res.Details)
	}
	if res.Terminate != nil {
		t.Error("validation failure must not terminate the run")
	}
}

func TestRegistryBadSchemaRejectedAtRegister(t *testing.T) {
	r := NewToolRegistry()
	err := r.Register(stubTool{name: "bad", schema: `{"type": 123}`})
	if err == nil {
		t.Fatal("expected invalid schema to fail at registration")
	}
}

func TestRegistryUnknownFieldNamesAcceptedAndSuggestion(t *testing.T) {
	r := newTestRegistry(t)
	errs := r.Validate("person", json.RawMessage(`{"name":"ada","nmae":"x"}`))
	if len(errs) != 1 {
		t.Fatalf("want one error, got %+v", errs)
	}
	if errs[0].Field != "/nmae" {
		t.Errorf("field = %q, want /nmae", errs[0].Field)
	}
	for _, want := range []string{
		`unknown field "nmae"`,
		"accepted: age, name",
		`did you mean "name"?`,
	} {
		if !strings.Contains(errs[0].Message, want) {
			t.Errorf("message %q missing %q", errs[0].Message, want)
		}
	}
}

func TestRegistryUnknownFieldGivesBashAliasHint(t *testing.T) {
	r := NewToolRegistry()
	schema := `{
	  "type": "object",
	  "properties": {
	    "command": {"type": "string"},
	    "justification": {"type": "string"}
	  },
	  "required": ["command"],
	  "additionalProperties": false
	}`
	if err := r.Register(stubTool{name: "bash", schema: schema}); err != nil {
		t.Fatalf("register: %v", err)
	}
	errs := r.Validate("bash", json.RawMessage(`{"command":"ls","description":"list files"}`))
	if len(errs) != 1 {
		t.Fatalf("want one error, got %+v", errs)
	}
	if errs[0].Field != "/description" {
		t.Errorf("field = %q, want /description", errs[0].Field)
	}
	if !strings.Contains(errs[0].Message, `did you mean "justification"?`) {
		t.Errorf("message %q missing the bash alias hint", errs[0].Message)
	}
}

func TestRegistryUnknownNestedFieldIsLocated(t *testing.T) {
	r := NewToolRegistry()
	schema := `{
	  "type": "object",
	  "properties": {
	    "name": {"type": "string"},
	    "meta": {
	      "type": "object",
	      "properties": {"x": {"type": "string"}},
	      "additionalProperties": false
	    }
	  },
	  "required": ["name"],
	  "additionalProperties": false
	}`
	if err := r.Register(stubTool{name: "nested", schema: schema}); err != nil {
		t.Fatalf("register: %v", err)
	}
	errs := r.Validate("nested", json.RawMessage(`{"name":"a","meta":{"x":"1","description":"d"}}`))
	if len(errs) != 1 {
		t.Fatalf("want one error, got %+v", errs)
	}
	if errs[0].Field != "/meta/description" {
		t.Errorf("field = %q, want /meta/description", errs[0].Field)
	}
	if strings.Contains(errs[0].Message, "accepted:") {
		t.Errorf("nested message must not list root fields: %q", errs[0].Message)
	}
}

func TestRegistryRepairDropsMetadataField(t *testing.T) {
	r := newTestRegistry(t)
	repaired, notes, ok := r.RepairUnknownFields("person",
		json.RawMessage(`{"name":"ada","description":"a person"}`))
	if !ok {
		t.Fatal("expected the metadata field to be repaired")
	}
	if errs := r.Validate("person", repaired); len(errs) > 0 {
		t.Fatalf("repaired args still invalid: %+v", errs)
	}
	if strings.Contains(string(repaired), "description") {
		t.Errorf("repaired args still carry description: %s", repaired)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], `ignored unknown field "description"`) {
		t.Errorf("notes = %v, want the dropped description", notes)
	}
}

func TestRegistryRepairMapsBashDescriptionToJustification(t *testing.T) {
	r := NewToolRegistry()
	schema := `{
	  "type": "object",
	  "properties": {
	    "command": {"type": "string"},
	    "justification": {"type": "string"}
	  },
	  "required": ["command"],
	  "additionalProperties": false
	}`
	if err := r.Register(stubTool{name: "bash", schema: schema}); err != nil {
		t.Fatalf("register: %v", err)
	}
	repaired, notes, ok := r.RepairUnknownFields("bash",
		json.RawMessage(`{"command":"ls","description":"needed to inspect the tree"}`))
	if !ok {
		t.Fatal("expected description to be repaired")
	}
	var got map[string]string
	if err := json.Unmarshal(repaired, &got); err != nil {
		t.Fatalf("decode repaired args: %v", err)
	}
	if got["justification"] != "needed to inspect the tree" {
		t.Errorf("justification = %q, want the description text", got["justification"])
	}
	if len(notes) != 1 || !strings.Contains(notes[0], `mapped unknown field "description" to "justification"`) {
		t.Errorf("notes = %v, want the alias mapping", notes)
	}
}

func TestRegistryRepairRefusesSemanticOrInvalidFields(t *testing.T) {
	r := newTestRegistry(t)
	cases := map[string]string{
		"semantic field":   `{"name":"ada","cwd":"/tmp"}`,
		"typo":             `{"name":"ada","nmae":"x"}`,
		"missing required": `{"description":"a person"}`,
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, ok := r.RepairUnknownFields("person", json.RawMessage(args)); ok {
				t.Errorf("RepairUnknownFields(%s) repaired, want refused", args)
			}
		})
	}
}
