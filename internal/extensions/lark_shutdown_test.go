//go:build unix

package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
)

type larkFixtureEOFReader struct {
	io.ReadCloser
	once sync.Once
	eof  chan struct{}
}

func (r *larkFixtureEOFReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if err == io.EOF {
		r.once.Do(func() { close(r.eof) })
	}
	return n, err
}

func TestLarkStateDrainsCleanupOutputAfterInputEOF(t *testing.T) {
	a, _, ref := larkStateManagers(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := a.Connect(ctx, ref); err != nil {
		t.Fatal(err)
	}
	callCtx, cancelCall := context.WithCancel(agentcore.WithExternalToolAuthorization(ctx))
	defer cancelCall()
	done := make(chan agentcore.AgentToolResult, 1)
	go func() {
		result, _ := a.Tools()[0].Execute(callCtx, "shutdown-output", json.RawMessage(`{"hold":true,"cleanup_reply":true}`), nil)
		done <- result
	}()
	active := filepath.Join(sourceHome(ref), "active")
	waitLarkStateFile(t, ctx, active)
	cancelCall()
	select {
	case result := <-done:
		if !result.IsError {
			t.Fatal("cancelled call was reported successful")
		}
	case <-ctx.Done():
		t.Fatal("shutdown did not half-close input and allow cleanup to drain")
	}
	// A loaded runner may exceed the bounded drain wait. Wait for the lease,
	// not an arbitrary sleep, before asserting the handler finished cleanup.
	unlock, err := lockStateContext(ctx, filepath.Join(filepath.Dir(sourceHome(ref)), ".lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := os.Stat(active); !os.IsNotExist(err) {
		t.Fatalf("closing MCP transport killed cleanup before removing the active marker: %v", err)
	}
}

func TestLarkShutdownReportsAbnormalChildExit(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".lock")
	unlock, err := lockState(path)
	if err != nil {
		t.Fatal(err)
	}
	wait := make(chan struct{})
	close(wait)
	exitErr := errors.New("signal: broken pipe")
	child := &mcpBackend{wait: wait, waitErr: exitErr}
	err = closeLarkStateOperation(child, &stateOperationLease{path: path, unlock: unlock}, func() {})
	if !errors.Is(err, exitErr) || !strings.Contains(err.Error(), "did not exit cleanly") {
		t.Fatalf("abnormal exit was silently swallowed: %v", err)
	}
	release, err := lockState(path)
	if err != nil {
		t.Fatalf("confirmed child exit leaked its lease: %v", err)
	}
	release()
}
