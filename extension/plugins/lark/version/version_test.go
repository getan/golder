package version

import (
	"regexp"
	"testing"
)

func TestVersionIsStableSemver(t *testing.T) {
	if !regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`).MatchString(Version) {
		t.Fatalf("invalid stable Lark version %q", Version)
	}
}
