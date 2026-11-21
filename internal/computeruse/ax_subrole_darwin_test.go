//go:build darwin && cgo

package computeruse

import "testing"

func TestAXInputBlockingNeedsAnExplicitSecureSignal(t *testing.T) {
	for _, tc := range []struct {
		role, subrole, want string
		response            int
	}{
		{"AXTextField", "", "", 1},
		{"AXTextArea", "", "", 2},
		{"AXTextField", "AXSearchField", "", 0},
		{"AXTextField", "AXSecureTextField", "secure_field", 0},
		{"AXSecureTextField", "", "secure_field", 2},
		{"AXTextField", "", "text_subrole_unreadable", 3},
		{"AXTextField", "", "text_subrole_unreadable", 0},
		{"", "", "role_unreadable", 0},
		{"AXGroup", "", "", 2},
	} {
		got, ok := macAXBlockReason(tc.role, tc.subrole, tc.response)
		if !ok || got != tc.want {
			t.Fatalf("%s/%s blocked=%q want %q", tc.role, tc.subrole, got, tc.want)
		}
	}
}
