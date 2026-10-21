package ui

import "testing"

func TestDebugEnabled(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"", false},
		{"0", false},
		{"off", false},
		{"1", true},
		{"true", true},
		{"YES", true},
	} {
		t.Setenv("GOLDER_DEBUG", tc.value)
		if got := DebugEnabled(); got != tc.want {
			t.Errorf("GOLDER_DEBUG=%q => %v, want %v", tc.value, got, tc.want)
		}
	}
}
