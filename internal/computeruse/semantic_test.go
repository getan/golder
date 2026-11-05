package computeruse

import (
	"context"
	"errors"
	"testing"
)

type semanticTestBackend struct {
	called int
}

func (*semanticTestBackend) Validate(context.Context, Target, bool) error { return nil }
func (*semanticTestBackend) Close() error                                 { return nil }
func (*semanticTestBackend) Snapshot(context.Context, Target) (string, []Element, bool, error) {
	return "backend-epoch", []Element{{Ref: "@e1", Role: "button", Enabled: true}}, false, nil
}
func (*semanticTestBackend) SupportsAction(action string) bool { return action == "click" }
func (b *semanticTestBackend) Act(_ context.Context, _ Target, id, ref, action, _ string) error {
	if id != "backend-epoch" || ref != "@e1" || action != "click" {
		return errors.New("incorrect action binding")
	}
	b.called++
	return nil
}

func TestSemanticActionConsumesSnapshotAndCannotSurvivePause(t *testing.T) {
	ctx := context.Background()
	backend := &semanticTestBackend{}
	s := New(backend)
	if err := s.Start(ctx, Target{ID: "target"}, Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.Observe(ctx)
	if err != nil || snapshot.ID == "" || snapshot.ID == "backend-epoch" {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	if err := s.Act(ctx, snapshot.ID, "@other", "click", ""); !errors.Is(err, ErrStaleObservation) {
		t.Fatalf("foreign ref: %v", err)
	}
	if err := s.Act(ctx, snapshot.ID, "@e1", "click", ""); err != nil || backend.called != 1 {
		t.Fatalf("action: calls=%d err=%v", backend.called, err)
	}
	if err := s.Act(ctx, snapshot.ID, "@e1", "click", ""); !errors.Is(err, ErrStaleObservation) {
		t.Fatalf("reused ref: %v", err)
	}
	snapshot, err = s.Observe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s.Pause()
	if err := s.Resume(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Act(ctx, snapshot.ID, "@e1", "click", ""); !errors.Is(err, ErrStaleObservation) {
		t.Fatalf("pre-pause ref: %v", err)
	}
}
