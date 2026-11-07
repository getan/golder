package judge

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/permissions"
)

func TestGUITerminalStaticFloorInAllModes(t *testing.T) {
	for _, mode := range []permissions.Mode{permissions.ReadOnly, permissions.Ask, permissions.Auto, permissions.FullAccess} {
		gate := ConsentGate(ConsentOpts{Mode: permissions.New(mode), Cwd: t.TempDir()})
		for _, raw := range []string{
			`{"action":"send","pane":"p","text":"cat ~/.ssh/id_rsa"}`,
			`{"action":"new","argv":["sh","-c","cat ~/.ssh/id_rsa"]}`,
		} {
			dec := gate(context.Background(), agentcore.AgentToolCall{Name: "terminal", Arguments: json.RawMessage(raw)})
			if dec == nil || !dec.Block {
				t.Fatalf("mode %s accepted %s: %+v", mode, raw, dec)
			}
		}
	}
}

func TestGUITerminalReadOnlyIsActionScoped(t *testing.T) {
	for _, tc := range []struct {
		action string
		want   bool
	}{{"list", true}, {"capture", true}, {"wait", true}, {"send", false}, {"new", false}, {"revoke", false}} {
		raw, _ := json.Marshal(map[string]string{"action": tc.action})
		if got := readOnlyAllowedCall("terminal", raw); got != tc.want {
			t.Errorf("%s: got %t want %t", tc.action, got, tc.want)
		}
	}
}
