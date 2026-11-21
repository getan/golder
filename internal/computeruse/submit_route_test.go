package computeruse

import (
	"errors"
	"testing"
)

func TestFieldSubmitRoutes(t *testing.T) {
	failure := errors.New("refusal or unknown outcome")
	focus := staleReason("field_focus_unconfirmed", "test focus refusal")
	foreground := errors.New("foreground refused")
	for _, tc := range []struct {
		confirm, key, foreground, want error
		trace                          string
	}{
		{nil, nil, nil, nil, "C"},
		{failure, nil, nil, failure, "C"},
		{errNoConfirmAction, nil, nil, nil, "CK"},
		{errNoConfirmAction, focus, nil, nil, "CKFK"},
		{errNoConfirmAction, focus, foreground, foreground, "CKF"},
		{errNoConfirmAction, failure, nil, failure, "CK"},
	} {
		trace := ""
		keyCalls := 0
		key := func() error {
			trace += "K"
			keyCalls++
			if keyCalls == 1 {
				return tc.key
			}
			return nil
		}
		err := routeFieldSubmit(func() error { trace += "C"; return tc.confirm }, key, func() error { trace += "F"; return tc.foreground })
		if trace != tc.trace || !errors.Is(err, tc.want) {
			t.Fatalf("trace=%s err=%v, want %s %v", trace, err, tc.trace, tc.want)
		}
	}
}
