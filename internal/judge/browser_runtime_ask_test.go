package judge

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/grants/grantstest"
	"github.com/getan/golder/internal/permissions"
)

func TestBrowserRuntimeAskIsExplicitAndNotBashEscape(t *testing.T) {
	grantstest.Install(t)
	for _, allow := range []bool{false, true} {
		asked := 0
		gate := ConsentGate(ConsentOpts{Mode: permissions.New(permissions.Ask), Cwd: t.TempDir(), BrowserRuntimeOutside: true,
			Classifier: consentClassifier{v: Verdict{Level: Contain, Network: true}}, Sandboxed: func(string) bool { return true },
			Confirm: func(_ context.Context, req ConsentRequest) ConsentAnswer {
				asked++
				if !hasNeed(req.Needs, CapBrowserRuntime) || hasNeed(req.Needs, CapUnconfined) || req.CanContain {
					t.Fatalf("misleading browser question: %+v", req)
				}
				return ConsentAnswer{Answered: true, Allow: allow, Scope: ScopeOnce, Needs: req.Needs}
			},
		})
		dec := gate(context.Background(), agentcore.AgentToolCall{Name: "browser", Arguments: json.RawMessage(`{"action":"open","url":"about:blank"}`)})
		if asked != 1 || dec == nil {
			t.Fatalf("asked=%d decision=%+v", asked, dec)
		}
		if allow {
			if dec.Block || !dec.BrowserRuntime || dec.Direct || dec.Sandbox {
				t.Fatalf("approved browser envelope=%+v", dec)
			}
		} else if !dec.Block || dec.BrowserRuntime || dec.Direct {
			t.Fatalf("refusal granted authority: %+v", dec)
		}
	}
}

func TestBrowserRuntimeFailedReviewFailsClosed(t *testing.T) {
	grantstest.Install(t)
	gate := ConsentGate(ConsentOpts{Mode: permissions.New(permissions.Auto), Cwd: t.TempDir(), BrowserRuntimeOutside: true,
		Classifier: consentClassifier{v: Verdict{Level: Allow, Failed: true}}, Sandboxed: func(string) bool { return true },
		Confirm: func(context.Context, ConsentRequest) ConsentAnswer {
			t.Fatal("auto must not ask a human after failed review")
			return ConsentAnswer{}
		},
	})
	dec := gate(context.Background(), agentcore.AgentToolCall{Name: "browser", Arguments: json.RawMessage(`{"action":"open","url":"about:blank"}`)})
	if dec == nil || !dec.Block || dec.BrowserRuntime || dec.Direct {
		t.Fatalf("failed review granted authority: %+v", dec)
	}
}
