package githubmirror

import (
	"strings"
	"testing"
)

func TestPrefixesEnvironment(t *testing.T) {
	t.Setenv(Env, "")
	if got := Prefixes(); len(got) != len(Defaults) {
		t.Fatalf("defaults = %v", got)
	}
	t.Setenv(Env, "off")
	if got := Prefixes(); got != nil {
		t.Fatalf("off = %v", got)
	}
	t.Setenv(Env, "https://a.example/  https://b.example/")
	if got := Prefixes(); len(got) != 2 || got[0] != "https://a.example/" || got[1] != "https://b.example/" {
		t.Fatalf("override = %v", got)
	}
}

func TestApplyOnlyPrefixesGitHubURLs(t *testing.T) {
	url := "https://github.com/getan/golder/releases/download/v1.2.3/file.tar.gz"
	got := Apply(url, []string{"https://m1.example/", "https://m2.example/"})
	if len(got) != 3 || got[0] != url || got[1] != "https://m1.example/"+url || !strings.Contains(got[2], "m2.example") {
		t.Fatalf("apply = %v", got)
	}
	other := "https://example.invalid/file"
	if got := Apply(other, []string{"https://m1.example/"}); len(got) != 1 || got[0] != other {
		t.Fatalf("non-GitHub URL was mirrored: %v", got)
	}
}
