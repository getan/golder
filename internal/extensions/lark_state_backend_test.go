//go:build unix

package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getan/golder/extension"
)

type leasedLarkFixture struct {
	cat   Catalog
	call  func(context.Context, extension.Call) (extension.Result, error)
	close func() error
}

func (f *leasedLarkFixture) Catalog(context.Context) (Catalog, error) { return f.cat, nil }
func (f *leasedLarkFixture) Call(ctx context.Context, c extension.Call, _ func(extension.Progress)) (extension.Result, error) {
	return f.call(ctx, c)
}
func (f *leasedLarkFixture) GetPrompt(context.Context, string, map[string]string) (string, error) {
	return "prompt", nil
}
func (f *leasedLarkFixture) ReadResource(context.Context, string) (string, error) {
	return "resource", nil
}
func (f *leasedLarkFixture) Close() error {
	if f.close != nil {
		return f.close()
	}
	return nil
}

func larkStateTestBackend(t *testing.T, open func(context.Context, context.Context) (backend, error)) *larkStateBackend {
	t.Helper()
	cfg := SourceConfig{Transport: "stdio", StateDir: true, Command: "/native/golder-lark", stateHome: sourceHome(SourceRef{Kind: "mcp", ID: "lark"})}
	b := newLarkStateBackend(context.Background(), cfg, t.TempDir(), &logBuffer{}, func() {}, func() error { return nil })
	b.open = open
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func larkStateTestCatalog() Catalog {
	return Catalog{Tools: []extension.ToolSpec{{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)}}}
}

func TestLarkStateBackendQueuesWithoutHoldingIdleLease(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	var active, peak, opens, closes atomic.Int32
	entered, release := make(chan struct{}, 2), make(chan struct{})
	open := func(context.Context, context.Context) (backend, error) {
		opens.Add(1)
		n := active.Add(1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		return &leasedLarkFixture{cat: larkStateTestCatalog(), call: func(ctx context.Context, c extension.Call) (extension.Result, error) {
			if c.ID == "first" {
				entered <- struct{}{}
				select {
				case <-release:
				case <-ctx.Done():
					return extension.Result{}, ctx.Err()
				}
			}
			return extension.Text(c.ID), nil
		}, close: func() error { active.Add(-1); closes.Add(1); return nil }}, nil
	}
	a, b := larkStateTestBackend(t, open), larkStateTestBackend(t, open)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, backend := range []*larkStateBackend{a, b} {
		if _, err := backend.Catalog(ctx); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(filepath.Dir(a.cfg.stateHome), ".lock")
	unlock, err := lockState(path)
	if err != nil {
		t.Fatalf("idle logical connections retained a lifetime lock: %v", err)
	}
	unlock()
	firstDone := make(chan error, 1)
	go func() { _, err := a.Call(ctx, extension.Call{ID: "first", Tool: "echo"}, nil); firstDone <- err }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("first call did not start")
	}
	if _, err := lockState(path); !errors.Is(err, errStateInUse) {
		t.Fatalf("active call did not exclude setup/reset: %v", err)
	}
	queued := make(chan struct{}, 1)
	secondCtx, cancelSecond := context.WithCancel(ctx)
	secondDone := make(chan error, 1)
	go func() {
		_, err := b.Call(secondCtx, extension.Call{ID: "cancelled", Tool: "echo"}, func(extension.Progress) { queued <- struct{}{} })
		secondDone <- err
	}()
	select {
	case <-queued:
	case <-ctx.Done():
		t.Fatal("second call did not enter a cancellable queue")
	}
	cancelSecond()
	select {
	case err := <-secondDone:
		var before *larkNotDispatchedError
		if !errors.Is(err, context.Canceled) || !errors.As(err, &before) || opens.Load() != 3 {
			t.Fatalf("cancelled queue launched a child or lost not-dispatched classification: opens=%d err=%v", opens.Load(), err)
		}
	case <-ctx.Done():
		t.Fatal("cancelled queue did not return")
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if _, err := b.Call(ctx, extension.Call{ID: "second", Tool: "echo", PolicyApproved: true}, nil); err != nil {
		t.Fatal(err)
	}
	if peak.Load() != 1 || opens.Load() != closes.Load() {
		t.Fatalf("state writers overlapped or children leaked: peak=%d opens=%d closes=%d", peak.Load(), opens.Load(), closes.Load())
	}
}

func TestLarkStateBackendRevalidatesAfterQueueAndBeforeDispatch(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	var calls atomic.Int32
	b := larkStateTestBackend(t, func(context.Context, context.Context) (backend, error) {
		return &leasedLarkFixture{cat: larkStateTestCatalog(), call: func(context.Context, extension.Call) (extension.Result, error) {
			calls.Add(1)
			return extension.Text("ok"), nil
		}}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := b.Catalog(ctx); err != nil {
		t.Fatal(err)
	}
	if err := privateStateParent(b.cfg.stateHome); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockState(filepath.Join(filepath.Dir(b.cfg.stateHome), ".lock"))
	if err != nil {
		t.Fatal(err)
	}
	queued := make(chan struct{}, 1)
	var revoked atomic.Bool
	b.check = func() error {
		if revoked.Load() {
			return errors.New("source fingerprint changed while waiting")
		}
		return nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := b.Call(ctx, extension.Call{Tool: "echo"}, func(extension.Progress) { queued <- struct{}{} })
		done <- err
	}()
	select {
	case <-queued:
	case <-ctx.Done():
		t.Fatal("call did not queue")
	}
	revoked.Store(true)
	unlock()
	if err := <-done; err == nil || calls.Load() != 0 {
		t.Fatalf("queued call ignored revocation: calls=%d err=%v", calls.Load(), err)
	}
	revoked.Store(false)
	b.open = func(context.Context, context.Context) (backend, error) {
		return &leasedLarkFixture{cat: Catalog{Tools: []extension.ToolSpec{{Name: "echo", InputSchema: json.RawMessage(`{"type":"string"}`)}}}, call: func(context.Context, extension.Call) (extension.Result, error) {
			calls.Add(1)
			return extension.Text("bad"), nil
		}}, nil
	}
	if _, err := b.Call(ctx, extension.Call{Tool: "echo"}, nil); err == nil || calls.Load() != 0 {
		t.Fatal("per-call restart dispatched with a changed schema")
	}
}

func TestStateLeaseWaitsForConfirmedChildExit(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".lock")
	unlock, err := lockState(path)
	if err != nil {
		t.Fatal(err)
	}
	exited, cleaned := make(chan struct{}), make(chan struct{})
	lease := &stateOperationLease{path: path, exited: exited, unlock: unlock}
	if finishStateLease(lease, func() { close(cleaned) }) {
		t.Fatal("an unconfirmed child exit released the state lease")
	}
	if _, err := lockState(path); !errors.Is(err, errStateInUse) {
		t.Fatalf("another writer entered while cleanup was pending: %v", err)
	}
	close(exited)
	select {
	case <-cleaned:
	case <-time.After(time.Second):
		t.Fatal("child exit did not release pending cleanup")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	unlock2, err := lockStateContext(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	unlock2()
	unlock2() // Release is idempotent; never unlock a reused descriptor.
}

func TestLarkCallScopedPolicyDoesNotChangeOtherSources(t *testing.T) {
	base := SourceConfig{Transport: "stdio", Command: "/native/golder-lark", StateDir: true, stateHome: "/private/home"}
	if !callScopedLarkState(base) {
		t.Fatal("known Lark helper was not selected for call-scoped state")
	}
	for _, c := range []SourceConfig{
		{Transport: "stdio", Command: "/native/third-party-mcp", StateDir: true, stateHome: "/private/home"},
		{Transport: "http", Command: base.Command, StateDir: true, stateHome: base.stateHome},
		{Runtime: "rpc", Command: base.Command, StateDir: true, stateHome: base.stateHome},
		{Transport: "stdio", Command: base.Command},
	} {
		if callScopedLarkState(c) {
			t.Fatalf("unrelated source opted into Lark state policy: %+v", c)
		}
	}
}

func TestLarkStateBackendPreservesExactApprovalAndNeverReplays(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	want := extension.Call{ID: "exact-call", Tool: "echo", Arguments: json.RawMessage(`{"value":"reviewed"}`), OperatorConfirmed: true, PolicyApproved: true}
	var calls int
	b := larkStateTestBackend(t, func(context.Context, context.Context) (backend, error) {
		return &leasedLarkFixture{cat: larkStateTestCatalog(), call: func(_ context.Context, got extension.Call) (extension.Result, error) {
			calls++
			if got.ID != want.ID || got.Tool != want.Tool || string(got.Arguments) != string(want.Arguments) || got.OperatorConfirmed != want.OperatorConfirmed || got.PolicyApproved != want.PolicyApproved {
				t.Fatalf("call-scoped transport changed exact authorization: %+v", got)
			}
			return extension.Result{}, errors.New("connection lost after dispatch")
		}}, nil
	})
	if _, err := b.Catalog(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err := b.Call(context.Background(), want, nil)
	var before *larkNotDispatchedError
	if err == nil || errors.As(err, &before) || calls != 1 {
		t.Fatalf("unknown execution was replayed or mislabeled: calls=%d err=%v", calls, err)
	}
}
