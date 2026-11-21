package computeruse

import (
	"errors"
	"testing"
)

func TestFieldSubmitRoutes(t *testing.T) {
	failure := errors.New("refusal or unknown outcome")
	focus := staleReason("field_focus_unconfirmed", "test focus refusal")
	for _, tc := range []struct {
		confirm, key, want error
		trace              string
	}{
		{nil, nil, nil, "C"},
		{failure, nil, failure, "C"},
		{errNoConfirmAction, nil, nil, "CK"},
		{errNoConfirmAction, focus, focus, "CK"},
		{errNoConfirmAction, failure, failure, "CK"},
	} {
		trace := ""
		err := routeFieldSubmit(func() error { trace += "C"; return tc.confirm }, func() error { trace += "K"; return tc.key })
		if trace != tc.trace || !errors.Is(err, tc.want) {
			t.Fatalf("trace=%s err=%v, want %s %v", trace, err, tc.trace, tc.want)
		}
	}
}
