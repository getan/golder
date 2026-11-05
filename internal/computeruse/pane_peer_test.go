package computeruse

import (
	"context"
	"encoding/json"
	"testing"
)

type fakePaneAuthority struct{ reply map[string]any }

func (f fakePaneAuthority) Session() string { return "session" }
func (f fakePaneAuthority) PaneID() string  { return "agent" }
func (f fakePaneAuthority) CallInto(_ context.Context, verb string, params, out any) error {
	if verb != "pane-grants" || params.(map[string]any)["peer_start"] != uint64(123) {
		panic("peer identity was not bound")
	}
	b, _ := json.Marshal(f.reply)
	return json.Unmarshal(b, out)
}

func TestPanePeerRequiresKernelBoundExactPane(t *testing.T) {
	valid := map[string]any{"pane": true, "peer_pid": 42, "window": "viewer", "session": "session", "via": "pid"}
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
		ok   bool
	}{
		{"valid", func(map[string]any) {}, true},
		{"wrong window", func(m map[string]any) { m["window"] = "other" }, false},
		{"wrong session", func(m map[string]any) { m["session"] = "other" }, false},
		{"token is not kernel identity", func(m map[string]any) { m["via"] = "token" }, false},
		{"unbound old daemon", func(m map[string]any) { delete(m, "peer_pid") }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reply := make(map[string]any, len(valid))
			for k, v := range valid {
				reply[k] = v
			}
			tc.edit(reply)
			err := verifyOperatorPanePeer(context.Background(), fakePaneAuthority{reply}, "viewer", 42, 123)
			if (err == nil) != tc.ok {
				t.Fatalf("verification error=%v, want success=%t", err, tc.ok)
			}
		})
	}
}

type fakePaneListing struct{ windows []map[string]any }

func (fakePaneListing) Session() string { return "session" }
func (fakePaneListing) PaneID() string  { return "agent" }
func (f fakePaneListing) CallInto(_ context.Context, verb string, params, out any) error {
	if verb != "list-windows" || params.(map[string]any)["session"] != "session" {
		panic("pane listing was not scoped to the session")
	}
	data, _ := json.Marshal(map[string]any{"windows": f.windows})
	return json.Unmarshal(data, out)
}

func TestListedPaneRequiresExactUniqueLocalProcess(t *testing.T) {
	for _, tc := range []struct {
		name    string
		windows []map[string]any
		ok      bool
	}{
		{"exact", []map[string]any{{"window_id": "viewer", "pid": 42}}, true},
		{"wrong pid", []map[string]any{{"window_id": "viewer", "pid": 43}}, false},
		{"missing pid", []map[string]any{{"window_id": "viewer"}}, false},
		{"remote", []map[string]any{{"window_id": "viewer", "pid": 42, "host": "other"}}, false},
		{"duplicate window", []map[string]any{{"window_id": "viewer", "pid": 42}, {"window_id": "viewer", "pid": 42}}, false},
		{"duplicate pid", []map[string]any{{"window_id": "viewer", "pid": 42}, {"window_id": "other", "pid": 42}}, false},
		{"missing window", []map[string]any{{"window_id": "other", "pid": 43}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := verifyListedOperatorPane(context.Background(), fakePaneListing{tc.windows}, "viewer", 42, 123)
			if (err == nil) != tc.ok {
				t.Fatalf("verification error=%v, want success=%t", err, tc.ok)
			}
		})
	}
	if err := verifyListedOperatorPane(context.Background(), fakePaneListing{}, "viewer", 42, 0); err == nil {
		t.Fatal("a peer without a creation time was accepted")
	}
}
