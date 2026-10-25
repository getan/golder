package judge

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/permissions"
)

func TestFacadeStaticFloorAcrossAllModes(t *testing.T) {
	for _, tc := range []struct{ tool, args string }{
		{"tuios", `{"action":"run","command":"sudo whoami"}`},
		{"tuios", `{"action":"send","text":"cat ~/.ssh/id_rsa"}`},
		{"tuios", `{"action":"new","argv":["sh","-c","cat ~/.ssh/id_rsa"]}`},
		{"workspace", `{"action":"create","preset":"debug-fullstack","backend":"echo ok","frontend":"sudo whoami"}`},
		{"browser", `{"action":"open","url":"file:///etc/passwd"}`},
		{"browser", `{"action":"tab_new","url":"javascript:alert(1)"}`},
		{"workspace", `{"action":"create","preset":"review","url":" file:///etc/passwd"}`},
	} {
		for _, mode := range []permissions.Mode{permissions.Auto, permissions.Ask, permissions.ReadOnly, permissions.FullAccess} {
			gate := ConsentGate(ConsentOpts{Mode: permissions.New(mode), Confirm: func(context.Context, ConsentRequest) ConsentAnswer {
				t.Fatal("static denial asked a human")
				return ConsentAnswer{}
			}})
			d := gate(context.Background(), agentcore.AgentToolCall{Name: tc.tool, Arguments: json.RawMessage(tc.args)})
			if d == nil || !d.Block {
				t.Errorf("%s %s in %s bypassed static floor: %+v", tc.tool, tc.args, mode, d)
			}
		}
	}
}

func TestFacadeBoundaryPathsAndNetwork(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside.txt")
	for _, tc := range []struct {
		tool string
		args any
	}{
		{"tuios", map[string]any{"action": "run", "command": "cat '" + outside + "'"}},
		{"tuios", map[string]any{"action": "new", "argv": []string{"cat", outside}}},
		{"workspace", map[string]any{"action": "create", "preset": "debug-fullstack", "backend": "cat '" + outside + "'"}},
	} {
		raw, _ := json.Marshal(tc.args)
		paths := TouchedPaths(tc.tool, raw)
		if !strings.Contains(strings.Join(paths, "\n"), outside) {
			t.Errorf("%s lost outside path: %v", tc.tool, paths)
		}
	}
	for _, tc := range []struct {
		tool, args string
		want       bool
	}{
		{"browser", `{"action":"open","url":"http://localhost:3000"}`, true},
		{"browser", `{"action":"click","ref":"@e1"}`, true},
		{"browser", `{"action":"snapshot"}`, false},
		{"workspace", `{"action":"create","preset":"review"}`, true},
		{"tuios", `{"action":"run","network":"on","command":"echo hi"}`, true},
	} {
		if got := NetworkRequested(tc.tool, json.RawMessage(tc.args)); got != tc.want {
			t.Errorf("%s %s network=%v", tc.tool, tc.args, got)
		}
	}
}

func TestFacadeAutoNeverPrompts(t *testing.T) {
	for _, v := range []Verdict{{Level: Allow}, {Level: Confirm}, {Level: Contain}, {Level: Deny}, {Level: Confirm, Failed: true}} {
		spy := &firstPartySpy{v: v}
		gate := ConsentGate(ConsentOpts{Mode: permissions.New(permissions.Auto), Classifier: spy,
			Sandboxed: func(string) bool { return false },
			Confirm: func(context.Context, ConsentRequest) ConsentAnswer {
				t.Fatal("auto requested human approval")
				return ConsentAnswer{}
			},
		})
		call := agentcore.AgentToolCall{Name: "browser", Arguments: json.RawMessage(`{"action":"click","host_control":true,"ref":"@e1"}`)}
		d := gate(context.Background(), call)
		if v.Level == Allow || v.Level == Confirm && !v.Failed {
			if d == nil || d.Block || !d.HostControl || !d.Network {
				t.Errorf("approved auto call lost capabilities: %+v", d)
			}
		} else if d == nil || !d.Block {
			t.Errorf("unsafe/unavailable auto call ran: %+v", d)
		}
	}
}

func TestFacadeAskStillRequiresExplicitHostCapability(t *testing.T) {
	called := false
	gate := ConsentGate(ConsentOpts{Mode: permissions.New(permissions.Ask), Classifier: &firstPartySpy{v: Verdict{Level: Allow}},
		Confirm: func(_ context.Context, req ConsentRequest) ConsentAnswer {
			called = true
			if !hasNeed(req.Needs, CapHostControl) {
				t.Fatal("host capability was invisible in dialog")
			}
			return ConsentAnswer{Allow: true, Answered: true, Needs: req.Needs}
		},
	})
	d := gate(context.Background(), agentcore.AgentToolCall{Name: "tuios", Arguments: json.RawMessage(`{"action":"press","window":"user","keys":"Enter","host_control":true}`)})
	if !called || d == nil || !d.HostControl || d.Block {
		t.Fatalf("host capability not settled: %+v", d)
	}
}

func TestFacadeNewPreservesArgvAndContext(t *testing.T) {
	spy := &firstPartySpy{v: Verdict{Level: Allow}}
	raw := json.RawMessage(`{"action":"new","argv":["sh","-c","printf '%s' 'a b'"],"cwd":"/tmp/demo","network":"on"}`)
	(FirstParty{Next: spy}).Classify(context.Background(), "tuios", raw)
	if spy.tool != "tuios" || string(spy.args) != string(raw) {
		t.Fatalf("review lost real invocation: %s %s", spy.tool, spy.args)
	}
}

func TestFacadeEmptyShellStillNeedsCapabilityReview(t *testing.T) {
	spy := &firstPartySpy{v: Verdict{Level: Deny}}
	raw := json.RawMessage(`{"action":"new","cwd":"/outside-project","network":"on"}`)
	v := (FirstParty{Next: spy}).Classify(context.Background(), "tuios", raw)
	if v.Level != Deny || spy.tool != "tuios" || string(spy.args) != string(raw) {
		t.Fatalf("empty argv skipped review of lasting capabilities: %+v", v)
	}
}
