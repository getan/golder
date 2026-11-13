package judge

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/grants"
	"github.com/getan/golder/internal/grants/grantstest"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/toolchain"
)

func TestToolchainConsentIsScopedAndFailClosed(t *testing.T) {
	for _, level := range []Level{Allow, Contain, Deny} {
		t.Run(level.String(), func(t *testing.T) {
			grantstest.Install(t)
			root := filepath.Dir(consentOutside(t))
			plan, err := toolchain.BuildGo("go", "/usr/local/go/bin/go", "identity", toolchain.GoPaths{GOROOT: filepath.Join(root, "sdk"), GOMODCACHE: filepath.Join(root, "modules"), GOCACHE: filepath.Join(root, "build")})
			if err != nil {
				t.Fatal(err)
			}
			gate := ConsentGate(ConsentOpts{Mode: permissions.New(permissions.Auto), Cwd: t.TempDir(), Classifier: consentClassifier{v: Verdict{Level: level}}, Sandboxed: func(string) bool { return true }, ResolveToolchain: func(context.Context, string, string) (toolchain.Plan, error) { return plan, nil }})
			dec := gate(context.Background(), agentcore.AgentToolCall{Name: "bash", Arguments: json.RawMessage(`{"command":"go test ./...","profile":"go"}`)})
			if level == Allow {
				if dec.Block || dec.Toolchain == nil || !dec.Sandbox || dec.Direct || dec.Network || dec.Bare {
					t.Fatalf("wrong profile envelope: %+v", dec)
				}
				if grants.Allows(plan.WriteDirs[0], grants.Write) {
					t.Fatal("once grant leaked into the process table")
				}
			} else if dec == nil || !dec.Block || dec.Toolchain != nil {
				t.Fatalf("unapproved profile ran: %+v", dec)
			}
		})
	}
}

func TestAuthorizationDistinguishesUnansweredFromUserDenial(t *testing.T) {
	for _, answered := range []bool{true, false} {
		gate := ConsentGate(ConsentOpts{Mode: permissions.New(permissions.Ask), Cwd: t.TempDir(), Classifier: consentClassifier{v: Verdict{Level: Confirm}}, Confirm: func(context.Context, ConsentRequest) ConsentAnswer { return ConsentAnswer{Answered: answered} }})
		dec := gate(context.Background(), agentcore.AgentToolCall{Name: "bash", Arguments: json.RawMessage(`{"command":"echo hello"}`)})
		want := "user_declined"
		if !answered {
			want = "approval_unanswered"
		}
		if dec == nil || !dec.Block || dec.Authorization == nil || dec.Authorization.Reason != want {
			t.Fatalf("wrong denial: %+v", dec)
		}
	}
}

func TestToolchainAskSessionReuseAndRefusal(t *testing.T) {
	grantstest.Install(t)
	root := filepath.Dir(consentOutside(t))
	p, err := toolchain.BuildGo("go", "/usr/local/go/bin/go", "id", toolchain.GoPaths{GOROOT: filepath.Join(root, "sdk"), GOMODCACHE: filepath.Join(root, "modules"), GOCACHE: filepath.Join(root, "build")})
	if err != nil {
		t.Fatal(err)
	}
	asks := 0
	gate := ConsentGate(ConsentOpts{Mode: permissions.New(permissions.Ask), Cwd: t.TempDir(), Classifier: consentClassifier{v: Verdict{Level: Allow}}, ResolveToolchain: func(context.Context, string, string) (toolchain.Plan, error) { return p, nil }, Sandboxed: func(string) bool { return true }, Confirm: func(_ context.Context, req ConsentRequest) ConsentAnswer {
		asks++
		return ConsentAnswer{Answered: true, Allow: true, Needs: req.Needs, Scope: ScopeSession}
	}})
	call := agentcore.AgentToolCall{Name: "bash", Arguments: json.RawMessage(`{"command":"go test ./...","profile":"go"}`)}
	for range 2 {
		if dec := gate(context.Background(), call); dec.Block || dec.Toolchain == nil {
			t.Fatalf("profile failed: %+v", dec)
		}
	}
	if asks != 1 {
		t.Fatalf("capabilities asked %d times", asks)
	}
	if _, err := grants.Refuse(p.WriteDirs[0], grants.Write, grants.Session); err != nil {
		t.Fatal(err)
	}
	if dec := gate(context.Background(), call); !dec.Block || dec.Authorization.Reason != "profile_refused" {
		t.Fatalf("refused cache became allowed: %+v", dec)
	}
}
