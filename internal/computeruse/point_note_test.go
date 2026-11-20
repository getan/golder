package computeruse

import (
	"strings"
	"testing"
)

func TestPointInspectionNoteDistinguishesFailures(t *testing.T) {
	cases := []struct {
		name string
		obs  PointInspection
		want string
	}{
		{"no element", PointInspection{}, "no_ax_hit"},
		{"wrong window", PointInspection{Role: "AXGroup", NonSensitiveVerified: true}, "not_verified_for_this_window"},
		{"sensitive", PointInspection{Role: "AXTextField", WindowVerified: true}, "sensitive_or_unknown"},
		{"container", PointInspection{Role: "AXGroup", WindowVerified: true, NonSensitiveVerified: true}, "container_not_bound"},
		{"text not editable", PointInspection{Role: "AXTextField", WindowVerified: true, NonSensitiveVerified: true}, "text_control_not_editable"},
		{"bound", PointInspection{InputBound: true}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pointInspectionNote(tc.obs); !strings.Contains(got, tc.want) {
				t.Fatalf("note = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAXControlEligibility(t *testing.T) {
	if !axControlEligible(true, true, true, true, true, 1) {
		t.Fatal("verified control should be eligible")
	}
	if axControlEligible(true, false, true, true, true, 1) {
		t.Fatal("unverified point must not bind a control")
	}
	if axControlEligible(true, true, true, true, false, 1) {
		t.Fatal("changed region must not bind a control")
	}
}

func TestCoordinateFallbackEligibility(t *testing.T) {
	cases := []struct {
		name                               string
		role                               string
		member, safe, visible, nonce, same bool
		want                               bool
	}{
		{"container", "AXGroup", true, true, true, true, true, true},
		{"no AX hit", "", false, false, true, true, true, true},
		{"foreign hit", "AXGroup", false, true, true, true, true, false},
		{"sensitive hit", "AXTextField", true, false, true, true, true, false},
		{"obscured", "AXGroup", true, true, false, true, true, false},
		{"changed region", "AXGroup", true, true, true, true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := coordinateFallbackEligible(true, tc.role, tc.member, tc.safe, tc.visible, tc.nonce, 1, tc.same)
			if got != tc.want {
				t.Fatalf("eligible = %v, want %v", got, tc.want)
			}
		})
	}
}
