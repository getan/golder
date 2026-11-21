package computeruse

import (
	"errors"
	"testing"
)

func TestFieldSubmitRoutes(t *testing.T) {
	failure := errors.New("refusal or unknown outcome")
	for _, tc := range []struct {
		confirm, foreground, key error
		trace                    string
		want                     error
	}{
		{nil, nil, nil, "C", nil},
		{failure, nil, nil, "C", failure},
		{errNoConfirmAction, failure, nil, "CF", failure},
		{errNoConfirmAction, nil, nil, "CFK", nil},
		{errNoConfirmAction, nil, failure, "CFK", failure},
	} {
		trace := ""
		err := routeFieldSubmit(func() error { trace += "C"; return tc.confirm }, func() error { trace += "F"; return tc.foreground }, func() error { trace += "K"; return tc.key })
		if trace != tc.trace || !errors.Is(err, tc.want) {
			t.Fatalf("trace=%s err=%v, want %s %v", trace, err, tc.trace, tc.want)
		}
	}
}
