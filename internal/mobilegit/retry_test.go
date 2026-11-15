package mobilegit

import (
	"errors"
	"testing"
)

func TestTransientNetworkError(t *testing.T) {
	for _, text := range []string{
		"read tcp: read: software caused connection abort",
		"connection reset by peer",
		"unexpected EOF",
		"i/o timeout",
	} {
		if !transientNetworkError(errors.New(text)) {
			t.Fatalf("expected transient: %q", text)
		}
	}
	for _, text := range []string{
		"authentication required",
		"repository not found",
		"non-fast-forward update",
	} {
		if transientNetworkError(errors.New(text)) {
			t.Fatalf("unexpected transient: %q", text)
		}
	}
}
