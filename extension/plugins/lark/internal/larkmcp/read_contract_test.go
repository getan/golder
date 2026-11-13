package larkmcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestBaseReadsRequestInlineJSON(t *testing.T) {
	for _, tc := range []struct {
		name string
		call string
	}{
		{name: "list", call: `{"operation":"list","options":{"base_token":"base","table_id":"table"}}`},
		{name: "search", call: `{"operation":"search","options":{"base_token":"base","table_id":"table","json":{}}}`},
		{name: "get", call: `{"operation":"get","options":{"base_token":"base","table_id":"table","record_id":"rec"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeRunner{}
			p := New(Config{}, f)
			r := p.Call(context.Background(), "base_record_search", json.RawMessage(tc.call))
			if !r.OK || len(f.calls) != 1 || !strings.Contains(f.calls[0], "--format=json") {
				t.Fatalf("Base read did not request inline JSON: result=%+v calls=%v", r, f.calls)
			}
		})
	}
}

func TestMessageSearchMarksIDOnlyResultPartial(t *testing.T) {
	f := &fakeRunner{fn: func(_ string, _ []string, _ []byte) (Output, error) {
		return Output{Stdout: []byte(`{"ok":true,"data":{"message_ids":["m1"],"note":"failed to fetch message details; missing im:message:readonly"}}`)}, nil
	}}
	p := New(Config{}, f)
	r := p.Call(context.Background(), "message_search", json.RawMessage(`{"options":{"query":"RAG"}}`))
	if !r.OK || !r.Partial || r.Verification != "id_list_only" {
		t.Fatalf("ID-only message search was reported as complete: %+v", r)
	}
	data, _ := r.Data.(map[string]any)
	note, _ := data["note"].(string)
	if !strings.Contains(note, "im:message:readonly") {
		t.Fatalf("message search lost the degradation reason: %+v", r.Data)
	}
}
