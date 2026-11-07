package mygoui

import "testing"

func TestPrivateAuthorizationNavigationBoundary(t *testing.T) {
	start := "http://127.0.0.1:1234/mcp-start/random"
	for _, tc := range []struct {
		url  string
		want bool
	}{
		{"https://auth.example.test/login", true},
		{"http://127.0.0.1:1234/callback", true},
		{"http://127.0.0.1:4567/login", true},
		{"http://127.0.0.1:9999/private", false},
		{"http://example.test/insecure", false},
		{"https://user:password@example.test/", false},
		{"file:///etc/passwd", false},
		{"javascript:alert(1)", false},
	} {
		if got := privateAuthorizationNavigation(tc.url, start, "127.0.0.1:4567"); got != tc.want {
			t.Errorf("navigation %q = %t, want %t", tc.url, got, tc.want)
		}
	}
}
