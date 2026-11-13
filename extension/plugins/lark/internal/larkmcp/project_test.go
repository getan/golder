package larkmcp

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestProjectDecodeURLUsesDirectURLArgument(t *testing.T) {
	const projectURL = "https://tenant.feishu.cn/project/workitem/123"
	var got []string
	f := &fakeRunner{fn: func(_ string, args []string, _ []byte) (Output, error) {
		got = append([]string(nil), args...)
		return Output{Stdout: []byte(`{"ok":true,"data":{"project_key":"P","work_item_type":"story","work_item_id":"123"}}`)}, nil
	}}
	p := New(Config{}, f)
	raw := json.RawMessage(`{"operation":"decode_url","options":{"url":"` + projectURL + `"}}`)
	r := p.Call(approved(), "project_schema", raw)
	want := []string{"url", "decode", "--url=" + projectURL, "--format=json"}
	if !r.OK || r.ExecutionState != "completed" || !slices.Equal(got, want) {
		t.Fatalf("decode_url result=%+v args=%v, want args=%v", r, got, want)
	}
}

func TestProjectDecodeURLFailureIsSpecific(t *testing.T) {
	f := &fakeRunner{fn: func(_ string, _ []string, _ []byte) (Output, error) {
		return Output{Code: 1}, nil
	}}
	p := New(Config{}, f)
	raw := json.RawMessage(`{"operation":"decode_url","options":{"url":"https://tenant.feishu.cn/project/workitem/123"}}`)
	r := p.Call(approved(), "project_schema", raw)
	if r.OK || r.Error == nil || r.Error.Code != "PROJECT_URL_DECODE_FAILED" ||
		strings.Contains(r.Error.Message, "role") || !strings.Contains(r.Error.NextAction, "Project login") {
		t.Fatalf("decode_url failure was misclassified: %+v", r)
	}
}

func TestProjectExpectedFieldUsesWorkItemFieldsShape(t *testing.T) {
	data := map[string]any{"work_item_fields": []any{
		map[string]any{"key": "priority", "value": map[string]any{"label": "P0", "value": "0"}},
		map[string]any{"field_key": "owner", "value": []any{map[string]any{"id": "u1", "name": "Alice"}}},
	}}
	priority := projectField(data, "priority")
	if priority == nil || !projectFieldValueEqual(priority["value"], "P0") || projectFieldValueEqual(priority["value"], "P1") {
		t.Fatalf("priority fixture did not compare as expected: %+v", priority)
	}
	owner := projectField(data, "owner")
	if owner == nil || !projectFieldValueEqual(owner["value"], []any{"Alice"}) {
		t.Fatalf("owner fixture did not compare as expected: %+v", owner)
	}
	if projectField(data, "missing") != nil {
		t.Fatal("missing field matched unexpectedly")
	}
}

func TestProjectURLDecodeLongTailIsCompletedRead(t *testing.T) {
	f := &fakeRunner{}
	p := New(Config{}, f)
	raw := json.RawMessage(`{"service":"project","argv":["url","decode","--url=https://tenant.feishu.cn/project/workitem/123"],"confirm":true}`)
	r := p.Call(approved(), "cli_call", raw)
	if !r.OK || r.ExecutionState != "completed" || r.Verification == "receipt_only" || len(f.calls) != 1 {
		t.Fatalf("url decode read state = %+v calls=%v", r, f.calls)
	}
}
