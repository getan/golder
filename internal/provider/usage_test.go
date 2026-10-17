package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// stubUsageClient routes every request in one lookup to rt. The usage endpoints
// are remote by design, so the test substitutes the transport rather than binding
// a server.
func stubUsageClient(t *testing.T, rt roundTripFunc) {
	t.Helper()
	old := usageClientFor
	usageClientFor = func(string, string) *http.Client { return &http.Client{Transport: rt} }
	t.Cleanup(func() { usageClientFor = old })
}

func usageJSON(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

var usageNow = time.Date(2026, 10, 18, 4, 0, 0, 0, time.UTC)

// TestFetchUsageOpencodeGo pins the three windows: the percentage the endpoint
// reports and the reset time rendered relative to now.
func TestFetchUsageOpencodeGo(t *testing.T) {
	var gotPath, gotAuth string
	stubUsageClient(t, func(r *http.Request) (*http.Response, error) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		return usageJSON(`{"usage":{
			"rolling":{"status":"ok","percent":0,"resetsAt":"2026-10-18T04:12:39Z"},
			"weekly":{"status":"ok","percent":87,"resetsAt":"2026-10-19T00:00:00Z"},
			"monthly":{"status":"ok","percent":76,"resetsAt":"2026-11-09T07:06:24Z"}}}`), nil
	})
	out, err := FetchUsage(context.Background(), UsageRequest{
		Provider: "opencode-go",
		BaseURL:  "https://opencode.ai/zen/go/v1",
		APIKey:   "sk-test",
		Now:      usageNow,
	})
	if err != nil {
		t.Fatalf("FetchUsage: %v", err)
	}
	if gotPath != "/zen/go/v1/usage" {
		t.Errorf("path = %q, want /zen/go/v1/usage", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("auth = %q, want the bearer token", gotAuth)
	}
	for _, want := range []string{"usage · opencode-go", "5h", "0%", "87%", "76%", "resets in 12m", "resets in 20h00m"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
}

// TestFetchUsageCommandCode pins the billing report: dollars against the window
// caps, the monthly credit balance, and the plan the subscription names.
func TestFetchUsageCommandCode(t *testing.T) {
	stubUsageClient(t, func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/alpha/billing/credits":
			return usageJSON(`{"credits":{"monthlyCredits":68.9,"purchasedCredits":5,
				"monthlyCreditsGranted":70},
				"windowLimits":{"fiveHour":{"used":0.24,"cap":14,"resetAt":1792296463098},
				"weekly":{"used":1.1,"cap":35,"resetAt":1792844516740}}}`), nil
		case "/alpha/billing/subscriptions":
			return usageJSON(`{"success":true,"data":{"planId":"individual-goat",
				"status":"active","currentPeriodEnd":"2026-11-17T12:03:26Z"}}`), nil
		}
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("nope"))}, nil
	})
	out, err := FetchUsage(context.Background(), UsageRequest{
		Provider: "commandcode",
		// The provider's base URL carries /provider/v1; the billing routes live on
		// the origin, and the report must find them there.
		BaseURL: "https://api.commandcode.ai/provider/v1",
		APIKey:  "user_test",
		Now:     usageNow,
	})
	if err != nil {
		t.Fatalf("FetchUsage: %v", err)
	}
	for _, want := range []string{
		"usage · commandcode (individual-goat · active)",
		"$0.24 / $14.00", "1.7%",
		"$1.10 / $35.00", "3.1%",
		"$68.90 left of $70.00", "1.6% used",
		"renews", "top-up   $5.00",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
}

// TestFetchUsageDeepSeek pins the balance report.
func TestFetchUsageDeepSeek(t *testing.T) {
	stubUsageClient(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/user/balance" {
			t.Errorf("path = %q, want /user/balance", r.URL.Path)
		}
		return usageJSON(`{"is_available":true,"balance_infos":[
			{"currency":"CNY","total_balance":"98.07","granted_balance":"0.00","topped_up_balance":"98.07"}]}`), nil
	})
	out, err := FetchUsage(context.Background(), UsageRequest{
		Provider: "deepseek",
		BaseURL:  "https://api.deepseek.com",
		APIKey:   "sk-deepseek",
		Now:      usageNow,
	})
	if err != nil {
		t.Fatalf("FetchUsage: %v", err)
	}
	for _, want := range []string{"usage · deepseek (available)", "CNY 98.07", "topped up CNY 98.07"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "granted") {
		t.Errorf("a zero grant should not be listed:\n%s", out)
	}
}

// TestFetchUsageUnsupported: a provider with no endpoint is not an error the
// caller has to translate — FetchUsage says which error it is, and the hint names
// what is supported and why a relay cannot answer.
func TestFetchUsageUnsupported(t *testing.T) {
	_, err := FetchUsage(context.Background(), UsageRequest{Provider: "openai"})
	if !errors.Is(err, ErrUsageUnsupported) {
		t.Fatalf("err = %v, want ErrUsageUnsupported", err)
	}
	hint := UsageHint("openai")
	for _, want := range []string{"openai", "opencode-go", "commandcode", "deepseek", "relay", "wallet"} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint missing %q:\n%s", want, hint)
		}
	}
	for _, name := range []string{"opencode-go", "commandcode", "deepseek"} {
		if !UsageSupported(name) {
			t.Errorf("UsageSupported(%q) = false, want true", name)
		}
	}
	if UsageSupported("openai") || UsageSupported("opencode-zen") {
		t.Error("only the three documented providers should be supported")
	}
}

// TestFetchUsageReportsTheGatewayBody: a non-200 carries the gateway's own
// explanation, which is the difference between "expired key" and "route moved".
func TestFetchUsageReportsTheGatewayBody(t *testing.T) {
	stubUsageClient(t, func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Body:       io.NopCloser(strings.NewReader(`{"error":{"code":"api_key_expired"}}`)),
		}, nil
	})
	_, err := FetchUsage(context.Background(), UsageRequest{Provider: "opencode-go", APIKey: "k"})
	if err == nil {
		t.Fatal("FetchUsage succeeded, want an auth error")
	}
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "api_key_expired") {
		t.Errorf("err = %v, want the status and the body detail", err)
	}
}

// TestResetsIn pins the compact durations the report prints.
func TestResetsIn(t *testing.T) {
	for _, c := range []struct {
		after time.Duration
		want  string
	}{
		{12 * time.Minute, "resets in 12m"},
		{3*time.Hour + 5*time.Minute, "resets in 3h05m"},
		{50 * time.Hour, "resets in 2d2h"},
		{-time.Minute, "resets now"},
	} {
		if got := resetsIn(usageNow, usageNow.Add(c.after)); got != c.want {
			t.Errorf("resetsIn(+%s) = %q, want %q", c.after, got, c.want)
		}
	}
	if got := resetsIn(usageNow, time.Time{}); got != "" {
		t.Errorf("resetsIn(zero) = %q, want empty", got)
	}
}
