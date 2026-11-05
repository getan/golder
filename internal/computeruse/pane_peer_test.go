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
