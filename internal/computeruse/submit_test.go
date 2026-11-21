package computeruse

import (
	"context"
	"errors"
	"testing"
)

func TestSubmitConsumesOnlyAnEditableSingleLineRef(t *testing.T) {
	for _, tc := range []struct {
		role     string
		editable bool
		wantCall bool
	}{
		{"AXTextField", true, true},
		{"AXTextField", false, false},
		{"AXTextArea", true, false},
		{"AXButton", false, false},
	} {
		ctx := context.Background()
		b := &guardedSemanticBackend{element: Element{Ref: "@e1", Role: tc.role, Editable: tc.editable, Enabled: true}}
		s := New(b)
		if err := s.Start(ctx, Target{ID: "w"}, Capabilities{Observe: true, Control: true}); err != nil {
			t.Fatal(err)
		}
		snap, err := s.Observe(ctx)
		if err != nil {
			t.Fatal(err)
		}
		err = s.Act(ctx, snap.ID, "@e1", "submit", "")
		if tc.wantCall && (err != nil || b.called != 1) || !tc.wantCall && (!errors.Is(err, ErrNoInput) || b.called != 0) {
			t.Fatalf("%s editable=%v calls=%d err=%v", tc.role, tc.editable, b.called, err)
		}
		if err := s.Act(ctx, snap.ID, "@e1", "submit", ""); !errors.Is(err, ErrStaleObservation) {
			t.Fatalf("submit lease reused: %v", err)
		}
	}
}
