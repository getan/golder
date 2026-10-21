package judge

import (
	"encoding/json"
	"testing"
)

func TestTuiosReadOnlyIsActionAware(t *testing.T) {
	for _, tc := range []struct {
		action string
		want   bool
	}{
		{"list", true},
		{"capture", true},
		{"wait", true},
		{"run", false},
		{"send", false},
		{"new", false},
		{"focus", false},
		{"move", false},
		{"resize", false},
		{"rename", false},
		{"minimize", false},
		{"close", false},
	} {
		args, _ := json.Marshal(map[string]string{"action": tc.action})
		if got := readOnlyAllowedCall("tuios", args); got != tc.want {
			t.Errorf("action %s = %v, want %v", tc.action, got, tc.want)
		}
	}
	if readOnlyAllowedCall("tuios", nil) {
		t.Error("a tuios call without an action must not pass read-only")
	}
}
