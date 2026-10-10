package selfupdate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsReleaseVersion(t *testing.T) {
	cases := map[string]bool{
		"":        false,
		"dev":     false,
		"unknown": false,
		" dev ":   false,
		"v0.4.0":  true,
		"0.4.0":   true,
	}
	for in, want := range cases {
		if got := IsReleaseVersion(in); got != want {
			t.Errorf("IsReleaseVersion(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestUpdateAvailable(t *testing.T) {
	tests := []struct {
		name              string
		current, latest   string
		wantAvail, wantOK bool
	}{
		{"update available", "v0.3.1", "v0.4.0", true, true},
		{"patch update", "0.4.0", "0.4.1", true, true},
		{"already latest", "v0.4.0", "v0.4.0", false, true},
		{"current newer", "v0.5.0", "v0.4.0", false, true},
		{"prerelease latest", "v0.4.0", "v0.4.1-next", true, true},
		{"dev current not comparable", "dev", "v0.4.0", false, false},
		{"unknown current not comparable", "unknown", "v0.4.0", false, false},
		{"unparseable latest", "v0.4.0", "not-a-version", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			avail, ok := UpdateAvailable(tt.current, tt.latest)
			if avail != tt.wantAvail || ok != tt.wantOK {
				t.Errorf("UpdateAvailable(%q,%q) = (%v,%v), want (%v,%v)",
					tt.current, tt.latest, avail, ok, tt.wantAvail, tt.wantOK)
			}
		})
	}
}

func TestParseVersion(t *testing.T) {
	tests := []struct {
		in   string
		want [3]int
		ok   bool
	}{
		{"v1.2.3", [3]int{1, 2, 3}, true},
		{"1.2.3", [3]int{1, 2, 3}, true},
		{"0.4.0-next", [3]int{0, 4, 0}, true},
		{"1.2", [3]int{1, 2, 0}, true},
		{"", [3]int{}, false},
		{"vabc", [3]int{}, false},
	}
	for _, tt := range tests {
		got, ok := parseVersion(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("parseVersion(%q) = (%v,%v), want (%v,%v)", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestLatestTagSiteFirst(t *testing.T) {
	var githubHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/latest" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"tag":"v0.4.0","url":"https://github.com/getan/golder/releases/tag/v0.4.0"}`))
			return
		}
		githubHits++
		if r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Errorf("missing Accept header on GitHub fallback")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"tag_name":"v9.9.9"}`))
	}))
	defer srv.Close()

	// LatestTag builds the URLs from constants; use a transport that redirects
	// to the test server regardless of host.
	client := srv.Client()
	client.Transport = rewriteHost{base: srv.URL, rt: client.Transport}

	// current is older than the site's tag, so the site's answer is already an
	// upgrade and the cross-check is skipped: the common path stays one request.
	tag, err := LatestTag(context.Background(), client, "getan/golder", "v0.3.0")
	if err != nil {
		t.Fatalf("LatestTag: %v", err)
	}
	if tag != "v0.4.0" {
		t.Errorf("tag = %q, want v0.4.0 (from site endpoint)", tag)
	}
	if githubHits != 0 {
		t.Errorf("the site reported an upgrade, so GitHub must not be called (called %d times)", githubHits)
	}
}

// TestLatestTagCrossChecksStaleSite is the regression for the release-window
// bug: the site endpoint is edge-cached, so right after a release it can still
// answer with the previous tag. With current equal to that stale tag, the old
// code returned it and `golder update` printed "already up to date" while a
// newer release existed. The GitHub API is now the tie-breaker on exactly that
// path.
func TestLatestTagCrossChecksStaleSite(t *testing.T) {
	var githubHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/latest" {
			// The stale copy: one version behind the real latest.
			_, _ = w.Write([]byte(`{"tag":"v0.4.0","url":"https://github.com/getan/golder/releases/tag/v0.4.0"}`))
			return
		}
		githubHits++
		_, _ = w.Write([]byte(`{"tag_name":"v0.4.1"}`))
	}))
	defer srv.Close()

	client := srv.Client()
	client.Transport = rewriteHost{base: srv.URL, rt: client.Transport}

	tag, err := LatestTag(context.Background(), client, "getan/golder", "v0.4.0")
	if err != nil {
		t.Fatalf("LatestTag: %v", err)
	}
	if tag != "v0.4.1" {
		t.Errorf("tag = %q, want v0.4.1 (the newer of the two when the site is stale)", tag)
	}
	if githubHits != 1 {
		t.Errorf("GitHub cross-check hits = %d, want exactly 1", githubHits)
	}
}

// TestLatestTagCrossCheckKeepsSiteOnGitHubFailure pins the mainland-China
// contract: when the cross-check cannot reach GitHub (api.github.com is often
// blocked there), the site's answer still stands rather than the call failing.
func TestLatestTagCrossCheckKeepsSiteOnGitHubFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/latest" {
			_, _ = w.Write([]byte(`{"tag":"v0.4.0"}`))
			return
		}
		w.WriteHeader(http.StatusForbidden) // anonymous rate limit, or a blocked network
	}))
	defer srv.Close()

	client := srv.Client()
	client.Transport = rewriteHost{base: srv.URL, rt: client.Transport}

	tag, err := LatestTag(context.Background(), client, "getan/golder", "v0.4.0")
	if err != nil {
		t.Fatalf("LatestTag: %v", err)
	}
	if tag != "v0.4.0" {
		t.Errorf("tag = %q, want the site's v0.4.0 when GitHub is unreachable", tag)
	}
}

// TestLatestTagSourceBuildCrossChecks pins the source-build path: `golder
// update` from a source build installs whatever tag comes back (there is no
// version to compare against), so a stale site cache would install the
// previous release. The cross-check runs here too, and the newer tag wins;
// when GitHub is unreachable the site's answer stands (see the failure test
// above).
func TestLatestTagSourceBuildCrossChecks(t *testing.T) {
	var githubHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/latest" {
			_, _ = w.Write([]byte(`{"tag":"v0.4.0"}`))
			return
		}
		githubHits++
		_, _ = w.Write([]byte(`{"tag_name":"v0.4.1"}`))
	}))
	defer srv.Close()

	client := srv.Client()
	client.Transport = rewriteHost{base: srv.URL, rt: client.Transport}

	tag, err := LatestTag(context.Background(), client, "getan/golder", "dev")
	if err != nil {
		t.Fatalf("LatestTag: %v", err)
	}
	if tag != "v0.4.1" {
		t.Errorf("tag = %q, want v0.4.1 (a source build installs the newest tag, not a stale one)", tag)
	}
	if githubHits != 1 {
		t.Errorf("GitHub cross-check hits = %d, want exactly 1", githubHits)
	}
}

// TestNewerTag pins the tie-breaker: the higher tag wins, and a tag that cannot
// be parsed never displaces one that can, since only the latter is actionable.
func TestNewerTag(t *testing.T) {
	cases := []struct{ a, b, want string }{
		{"v0.4.0", "v0.4.1", "v0.4.1"},
		{"v0.4.1", "v0.4.0", "v0.4.1"},
		{"v0.4.0", "v0.4.0", "v0.4.0"},
		{"v0.10.0", "v0.9.9", "v0.10.0"},
		{"v1.2.14", "v1.2.15", "v1.2.15"},
		{"garbage", "v0.4.1", "v0.4.1"},
		{"v0.4.1", "garbage", "v0.4.1"},
		{"garbage", "also-garbage", "garbage"},
	}
	for _, c := range cases {
		if got := newerTag(c.a, c.b); got != c.want {
			t.Errorf("newerTag(%q, %q) = %q, want %q", c.a, c.b, got, c.want)
		}
	}
}

func TestLatestTagGitHubFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/latest" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Errorf("missing Accept header")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"tag_name":"v0.4.1","name":"golder 0.4.1"}`))
	}))
	defer srv.Close()

	client := srv.Client()
	client.Transport = rewriteHost{base: srv.URL, rt: client.Transport}

	// current is older than the site's answer, so the site is authoritative and
	// the GitHub path is never reached here; the case that does reach it is
	// TestLatestTagCrossChecksStaleSite.
	tag, err := LatestTag(context.Background(), client, "getan/golder", "v0.3.0")
	if err != nil {
		t.Fatalf("LatestTag: %v", err)
	}
	if tag != "v0.4.1" {
		t.Errorf("tag = %q, want v0.4.1 (from GitHub API fallback)", tag)
	}
}

func TestLatestTagErrors(t *testing.T) {
	t.Run("non-200 from both", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()
		client := srv.Client()
		client.Transport = rewriteHost{base: srv.URL, rt: client.Transport}
		if _, err := LatestTag(context.Background(), client, "getan/golder", "v0.4.0"); err == nil {
			t.Fatal("expected error on 403")
		}
	})

	t.Run("empty tag from both", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/latest" {
				_, _ = w.Write([]byte(`{"tag":""}`))
				return
			}
			_, _ = w.Write([]byte(`{"tag_name":""}`))
		}))
		defer srv.Close()
		client := srv.Client()
		client.Transport = rewriteHost{base: srv.URL, rt: client.Transport}
		if _, err := LatestTag(context.Background(), client, "getan/golder", "v0.4.0"); err == nil {
			t.Fatal("expected error on empty tag_name")
		}
	})
}

// rewriteHost redirects every request to base, so tests can point the fixed
// GitHub API URL at an httptest server.
type rewriteHost struct {
	base string
	rt   http.RoundTripper
}

func (rw rewriteHost) RoundTrip(req *http.Request) (*http.Response, error) {
	u, err := req.URL.Parse(rw.base)
	if err != nil {
		return nil, err
	}
	req.URL.Scheme = u.Scheme
	req.URL.Host = u.Host
	rt := rw.rt
	if rt == nil {
		rt = http.DefaultTransport
	}
	return rt.RoundTrip(req)
}
