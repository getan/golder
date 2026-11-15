package mobilebuild

import "testing"

func TestParseGitHubURL(t *testing.T) {
	for _, raw := range []string{
		"https://github.com/getan/golder-mobile-test.git",
		"git@github.com:getan/golder-mobile-test.git",
		"ssh://git@github.com/getan/golder-mobile-test",
	} {
		owner, repo, ok := parseGitHubURL(raw)
		if !ok || owner != "getan" || repo != "golder-mobile-test" {
			t.Fatalf("%q => %q/%q ok=%t", raw, owner, repo, ok)
		}
	}
	if _, _, ok := parseGitHubURL("https://gitlab.com/getan/repo.git"); ok {
		t.Fatal("non-GitHub URL accepted")
	}
}
