package judge

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/grants/grantstest"
	"github.com/getan/golder/internal/permissions"
)

func TestBrowserRuntimeAutoIsReviewedWithoutHumanPrompt(t *testing.T) {
	grantstest.Install(t)
	for _, level := range []Level{Allow, Confirm, Contain, Deny} {
		opts := ConsentOpts{Mode: permissions.New(permissions.Auto), Cwd: t.TempDir(), BrowserRuntimeOutside: true,
			Classifier: consentClassifier{v: Verdict{Level: level, Network: true}}, Sandboxed: func(string) bool { return true },
			Confirm: func(context.Context, ConsentRequest) ConsentAnswer {
				t.Fatal("auto must not prompt a person")
				return ConsentAnswer{}
			},
		}
		call := agentcore.AgentToolCall{Name: "browser", Arguments: json.RawMessage(`{"action":"open","url":"about:blank"}`)}
		dec := ConsentGate(opts)(context.Background(), call)
		if level == Allow || level == Confirm {
			if dec == nil || dec.Block || !dec.BrowserRuntime || dec.Direct || dec.Sandbox {
				t.Fatalf("approved browser decision=%+v", dec)
			}
		} else if dec == nil || !dec.Block {
			t.Fatalf("contain/deny granted browser exception: %+v", dec)
		}
		opts.Enforce = true
		if d := ConsentGate(opts)(context.Background(), call); d == nil || !d.Block {
			t.Fatal("enforce permitted browser runtime exception")
		}
	}
}

func TestBrowserRuntimeDoesNotAuthorizeBashOrArbitraryPaneArgv(t *testing.T) {
	grantstest.Install(t)
	gate := ConsentGate(ConsentOpts{Mode: permissions.New(permissions.Auto), Cwd: t.TempDir(), BrowserRuntimeOutside: true,
		Classifier: consentClassifier{v: Verdict{Level: Allow, Network: true}}})
	for _, call := range []agentcore.AgentToolCall{
		{Name: "bash", Arguments: json.RawMessage(`{"command":"pwd","action":"open","browser_runtime":true}`)},
		{Name: "tuios", Arguments: json.RawMessage(`{"action":"new","argv":["golder","browse"],"browser_runtime":true}`)},
	} {
		if d := gate(context.Background(), call); d != nil && d.BrowserRuntime {
			t.Fatal("model arguments granted browser authority to a non-browser operation")
		}
	}
}
