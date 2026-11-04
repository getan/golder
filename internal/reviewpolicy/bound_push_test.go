package reviewpolicy

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/jev"
	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/permissions"
)

type unexpectedPushReviewer struct{ calls int }

func (r *unexpectedPushReviewer) Classify(context.Context, string, json.RawMessage) judge.Verdict {
	r.calls++
	return judge.Verdict{Level: judge.Contain, Source: "test"}
}

func TestBoundHTTPSPushUsesSameRuleWithDecisionModelRouter(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("BASH_ENV", "")
	t.Setenv("ENV", "")
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "https://example.com/group/repo.git"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	args, err := json.Marshal(map[string]string{"command": "git -C " + filepath.Base(repo) + " push origin main", "network": "on"})
	if err != nil {
		t.Fatal(err)
	}
	fallback := &unexpectedPushReviewer{}
	router := &Classifier{
		Client:   jev.New(jev.Config{Endpoint: "http://127.0.0.1:1", APIKey: "test", Timeout: 20 * time.Millisecond, MaxRetries: -1}),
		Fallback: fallback,
	}
	gate := judge.ConsentGate(judge.ConsentOpts{
		Mode: permissions.New(permissions.Auto), Cwd: filepath.Dir(repo), Classifier: router,
		Sandboxed: func(string) bool { return true },
	})
	decision := gate(context.Background(), agentcore.AgentToolCall{ID: "push", Name: "bash", Arguments: args})
	if decision == nil || decision.Block || !decision.Direct || !decision.Network || decision.ExecutionBinding == "" || fallback.calls != 0 {
		t.Fatalf("decision=%+v fallback calls=%d", decision, fallback.calls)
	}
}
