package computeruse

import (
	"context"
	"errors"
	"testing"
	"time"
)

type testBackend struct{ invalid bool }

func (b *testBackend) Validate(_ context.Context, _ Target, _ bool) error {
	if b.invalid {
		return errors.New("window changed")
	}
	return nil
}
func (*testBackend) Close() error { return nil }

func TestSessionAuthorizationAndTargetChanges(t *testing.T) {
	ctx := context.Background()
	b := &testBackend{}
	s := New(b)
	target := Target{ID: "one", Application: "Editor", Window: "Document"}
	if err := s.Start(ctx, target, Capabilities{Observe: true}); err != nil {
		t.Fatal(err)
	}
	called := false
	op := func(context.Context, Backend, Target) error { called = true; return nil }
	if err := s.Do(ctx, true, op); !errors.Is(err, ErrNotAllowed) || called {
		t.Fatalf("read-only control: called=%t err=%v", called, err)
	}
	if err := s.Start(ctx, Target{ID: "other"}, Capabilities{Observe: true}); err == nil {
		t.Fatal("switched target without stopping and reauthorizing")
	}
	b.invalid = true
	if err := s.Do(ctx, false, op); !errors.Is(err, ErrTargetLost) || !s.Status().Paused || called {
		t.Fatalf("invalid target: called=%t status=%+v err=%v", called, s.Status(), err)
	}
	b.invalid = false
	if err := s.Do(ctx, false, op); !errors.Is(err, ErrPaused) {
		t.Fatalf("paused operation: %v", err)
	}
	if err := s.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Do(ctx, false, op); err != nil || !called {
		t.Fatalf("authorized observation: called=%t err=%v", called, err)
	}
	s.Stop()
	if err := s.Do(ctx, false, op); !errors.Is(err, ErrNotActive) {
		t.Fatalf("stopped operation: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPauseCancelsInFlightOperation(t *testing.T) {
	s := New(&testBackend{})
	if err := s.Start(context.Background(), Target{ID: "one"}, Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- s.Do(context.Background(), true, func(ctx context.Context, _ Backend, _ Target) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	<-started
	s.Pause()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("operation returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("pause did not cancel the in-flight operation")
	}
	if !s.Status().Paused {
		t.Fatal("pause did not persist")
	}
}
