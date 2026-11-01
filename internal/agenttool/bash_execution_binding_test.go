package agenttool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

func TestBashRefusesStaleExecutionBindingBeforeSpawn(t *testing.T) {
	target := filepath.Join(t.TempDir(), "must-not-run")
	tool := &BashTool{Dir: t.TempDir()}
	ctx := agentcore.WithDirectGrant(context.Background())
	ctx = agentcore.WithExecutionBinding(ctx, "git-push-v1:"+string(make([]byte, 64)))
	args, _ := json.Marshal(map[string]any{"command": "touch " + target})
	result, err := tool.Execute(ctx, "binding", args, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("stale binding was accepted: %+v", result)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("command ran before execution binding verification")
	}
}
