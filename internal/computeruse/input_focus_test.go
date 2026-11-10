package computeruse

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestInputFocusAlreadyFocusedDoesNotRaiseWindow(t *testing.T) {
	raised := 0
	changed, err := prepareInputFocus(context.Background(), time.Second, time.Millisecond,
		func() (bool, error) { return true, nil },
		func() error { raised++; return nil })
	if changed || err != nil || raised != 0 {
		t.Fatalf("focused target should be untouched: changed=%t raised=%d err=%v", changed, raised, err)
	}
}

func TestInputFocusRestoresOnceAndWaitsForProof(t *testing.T) {
	raised, checks := 0, 0
	changed, err := prepareInputFocus(context.Background(), time.Second, time.Millisecond,
		func() (bool, error) { checks++; return checks >= 3, nil },
		func() error { raised++; return nil })
	if !changed || err != nil || raised != 1 || checks != 3 {
		t.Fatalf("restore: changed=%t raised=%d checks=%d err=%v", changed, raised, checks, err)
	}
}

func TestInputFocusUnprovenTargetCannotBeActivated(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		raised, checks := 0, 0
		changed, err := prepareInputFocus(context.Background(), time.Second, time.Millisecond,
			func() (bool, error) {
				checks++
				if checks == failAt {
					return false, ErrTargetLost
				}
				return false, nil
			}, func() error { raised++; return nil })
		if !errors.Is(err, ErrTargetLost) || raised != failAt-1 || changed != (failAt == 2) {
			t.Fatalf("target loss at check %d: raised=%d changed=%t err=%v", failAt, raised, changed, err)
		}
	}
}

func TestInputFocusUnavailableDoesNotRepeatedlyStealFocus(t *testing.T) {
	raised := 0
	changed, err := prepareInputFocus(context.Background(), 10*time.Millisecond, time.Millisecond,
		func() (bool, error) { return false, nil },
		func() error { raised++; return nil })
	if !changed || raised != 1 || !errors.Is(err, ErrStaleObservation) || errors.Is(err, ErrTargetLost) {
		t.Fatalf("focus timeout must not pause or repeatedly activate: raised=%d changed=%t err=%v", raised, changed, err)
	}
}

func TestInputFocusCancellationBeforeRestore(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	raised := 0
	_, err := prepareInputFocus(ctx, time.Second, time.Millisecond,
		func() (bool, error) { cancel(); return false, nil },
		func() error { raised++; return nil })
	if !errors.Is(err, context.Canceled) || raised != 0 {
		t.Fatalf("canceled control restored focus: raised=%d err=%v", raised, err)
	}
}

func TestInputFocusPauseOrStopCancelsPreparation(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "pause", true: "stop"}[stop], func(t *testing.T) {
			s := New(&testBackend{})
			if err := s.Start(context.Background(), Target{ID: "window"}, Capabilities{Observe: true, Control: true}); err != nil {
				t.Fatal(err)
			}
			raised, done := make(chan struct{}), make(chan error, 1)
			inputSent := false
			go func() {
				done <- s.Do(context.Background(), true, func(ctx context.Context, _ Backend, _ Target) error {
					_, err := prepareInputFocus(ctx, time.Second, time.Millisecond,
						func() (bool, error) { return false, nil },
						func() error { close(raised); return nil })
					if err == nil {
						inputSent = true
					}
					return err
				})
			}()
			select {
			case <-raised:
			case <-time.After(3 * time.Second):
				t.Fatal("focus preparation never started")
			}
			if stop {
				s.Stop()
			} else {
				s.Pause()
			}
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) || inputSent {
					t.Fatalf("pause/stop did not cancel input preparation: sent=%t err=%v", inputSent, err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("pause/stop did not interrupt the focus wait")
			}
			called := false
			err := s.Do(context.Background(), true, func(context.Context, Backend, Target) error { called = true; return nil })
			if called || (stop && !errors.Is(err, ErrNotActive)) || (!stop && !errors.Is(err, ErrPaused)) {
				t.Fatalf("automatic control reopened after pause/stop: called=%t err=%v", called, err)
			}
		})
	}
}

func TestInputFocusRestoreFailureIsNotRetried(t *testing.T) {
	raised := 0
	denied := errors.New("activation refused")
	_, err := prepareInputFocus(context.Background(), time.Second, time.Millisecond,
		func() (bool, error) { return false, nil },
		func() error { raised++; return denied })
	if !errors.Is(err, denied) || raised != 1 {
		t.Fatalf("failed activation retried: raised=%d err=%v", raised, err)
	}
}

func TestInputFocusCanceledDuringRestoreCannotSendInput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	checks := 0
	_, err := prepareInputFocus(ctx, time.Second, time.Millisecond,
		func() (bool, error) { checks++; return checks > 1, nil },
		func() error { cancel(); return nil })
	if !errors.Is(err, context.Canceled) || checks != 1 {
		t.Fatalf("canceled activation continued: checks=%d err=%v", checks, err)
	}
}

func TestInputFocusReadOnlySessionDoesNotPrepareInput(t *testing.T) {
	s := New(&testBackend{})
	if err := s.Start(context.Background(), Target{ID: "window"}, Capabilities{Observe: true}); err != nil {
		t.Fatal(err)
	}
	prepared := false
	err := s.Do(context.Background(), true, func(ctx context.Context, _ Backend, _ Target) error {
		prepared = true
		_, err := prepareInputFocus(ctx, time.Second, time.Millisecond,
			func() (bool, error) { return false, nil }, func() error { return nil })
		return err
	})
	if !errors.Is(err, ErrNotAllowed) || prepared {
		t.Fatalf("observation-only grant prepared physical input: prepared=%t err=%v", prepared, err)
	}
}
