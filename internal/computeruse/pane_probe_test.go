package computeruse

import "testing"

func TestPaneGraphicsProbeRequiresOurSuccessfulReply(t *testing.T) {
	for _, tc := range []struct {
		reply string
		want  bool
	}{
		{"\x1b_Gi=31;OK\x1b\\", true},
		{"\x1b_Gi=31;ERROR\x1b\\", false},
		{"\x1b_Gi=32;OK\x1b\\", false},
		{"\x1b_Gi=31;OK", false},
		{"", false},
	} {
		if got := paneGraphicsReply(tc.reply); got != tc.want {
			t.Errorf("reply %q: got %t, want %t", tc.reply, got, tc.want)
		}
	}
}
