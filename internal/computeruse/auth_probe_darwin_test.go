//go:build darwin && cgo

package computeruse

import "testing"

func TestAuthProbe(t *testing.T) {
	bits := authProbeBits()
	t.Logf("SLSEventAuthenticationMessage probe bits=%d (class=1 selector=2 setter=4 record=8 message=16)", bits)
	if methods := authProbeMethods(); methods != "" {
		t.Logf("class methods:\n%s", methods)
	}
}
