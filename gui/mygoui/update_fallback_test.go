package mygoui

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestGUIReleaseFallbackDoesNotBypassPackageVerification(t *testing.T) {
	for _, tc := range []struct {
		name, siteBody, wantTag, wantErr string
		cached                           bool
	}{
		{"current", `{"tag":"v1.2.3"}`, "v1.2.3", "", true},
		{"older", `{"tag_name":"v1.2.2"}`, "v1.2.2", "", true},
		{"newer", `{"tag":"v1.2.4"}`, "", "verified package metadata is unavailable", false},
		{"invalid", `{"tag":"v1.2.3/../../bad"}`, "", "rate limit exceeded", false},
		{"malformed", `{`, "", "rate limit exceeded", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			client := &http.Client{Transport: guiRoundTrip(func(r *http.Request) (*http.Response, error) {
				calls = append(calls, r.URL.Host)
				if r.Header.Get("Authorization") != "" {
					t.Fatal("unexpected credentials in update request")
				}
				status, body := http.StatusForbidden, "denied"
				header := make(http.Header)
				if r.URL.Host == "api.github.com" {
					header.Set("X-RateLimit-Remaining", "0")
				} else if r.URL.Host == "golder-cli.pages.dev" {
					status, body = http.StatusOK, tc.siteBody
				} else {
					t.Fatalf("unexpected host %q", r.URL.Host)
				}
				return &http.Response{StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)), Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})}
			release, cached, err := fetchGUIReleaseWithFallback(context.Background(), client, "v1.2.3")
			if cached != tc.cached || release.Tag != tc.wantTag || (tc.wantErr == "" && err != nil) || (tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr))) {
				t.Fatalf("release=%+v cached=%t err=%v", release, cached, err)
			}
			if strings.Join(calls, ",") != "api.github.com,golder-cli.pages.dev" {
				t.Fatalf("unexpected requests: %v", calls)
			}
			if cached && len(release.Assets) != 0 {
				t.Fatalf("cached tag produced installable assets: %+v", release)
			}
		})
	}
}

func TestGUIReleaseFallbackNotUsedOnSuccessOrCancellation(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelled), func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: guiRoundTrip(func(r *http.Request) (*http.Response, error) {
				calls++
				status, body := http.StatusOK, `{"tag_name":"v1.2.3"}`
				if cancelled {
					status, body = http.StatusForbidden, "denied"
				}
				return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelled {
				cancel()
			}
			_, cached, _ := fetchGUIReleaseWithFallback(ctx, client, "v1.2.3")
			if calls != 1 || cached {
				t.Fatalf("calls=%d cached=%t", calls, cached)
			}
		})
	}
}

func TestBannerMessageCanBeSelectedAndCopiedInFull(t *testing.T) {
	a := testApp()
	a.banner = "Update check failed: sample error"
	tt := ui.NewTester(a.view, 700, 500)
	row, ok := tt.Find(a.banner)
	if !ok {
		t.Fatal("banner text missing")
	}
	tt.Press(row.X+1, row.Y+row.H/2)
	tt.Move(row.X+row.W-1, row.Y+row.H/2)
	tt.Release(row.X+row.W-1, row.Y+row.H/2)
	tt.Command("copy")
	if got := tt.Clipboard(); got != a.banner {
		t.Fatalf("selected banner copied %q, want %q", got, a.banner)
	}
	a.banner = "Update check failed: " + strings.Repeat("diagnostic details ", 30)
	a.bannerErr = true
	tt.Frame()
	if err := tt.Click("Copy message"); err != nil {
		t.Fatal(err)
	}
	if got := tt.Clipboard(); got != a.banner {
		t.Fatalf("copied %q, want full message %q", got, a.banner)
	}
	if err := tt.Click("Dismiss"); err != nil || a.banner != "" {
		t.Fatalf("dismiss failed: %v, banner=%q", err, a.banner)
	}
}
