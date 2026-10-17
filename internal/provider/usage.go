package provider

// Subscription usage: what is left of the account's quota, per provider.
//
// Three providers expose a read-only endpoint that answers this with the same API
// key the conversation uses:
//
//   - opencode-go: GET {base}/usage — rolling 5h, weekly and monthly windows as
//     percentages with their reset times.
//   - commandcode: GET https://api.commandcode.ai/alpha/billing/credits (and
//     /subscriptions) — the 5h and weekly windows as dollars used against a cap,
//     the monthly credit balance, and the plan's billing period.
//   - deepseek: GET https://api.deepseek.com/user/balance — the account's
//     prepaid/ granted balance.
//
// Everything else has no such endpoint, and the report says so instead of
// guessing. That includes relays: a sub2api deployment answers /v1/usage with its
// own wallet balance and local request stats, which is the relay's ledger, not the
// upstream subscription's remaining quota — presenting it as the latter would be a
// comfortable lie.
//
// The opencode and commandcode routes are undocumented (the CLI uses them; the
// public docs do not list them), so a failure here is reported as "unavailable"
// and never blocks a turn.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrUsageUnsupported reports a provider with no usage endpoint.
var ErrUsageUnsupported = errors.New("no quota endpoint is known for this provider")

// UsageRequest is one usage lookup: the provider identity, the effective base
// URL, and the credential the conversation is already using.
type UsageRequest struct {
	Provider string
	BaseURL  string
	APIKey   string
	// Now defaults to time.Now; tests set it so reset times render stably.
	Now time.Time
}

// UsageSupported reports whether a provider has a usage endpoint, so a caller can
// decide whether to try before it builds a credential.
func UsageSupported(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "opencode-go", "commandcode", "deepseek":
		return true
	}
	return false
}

// FetchUsage queries the account's quota and renders it as the text a slash
// command prints. It returns ErrUsageUnsupported (wrapped with the provider name)
// for a provider that has no endpoint.
func FetchUsage(ctx context.Context, req UsageRequest) (string, error) {
	name := strings.ToLower(strings.TrimSpace(req.Provider))
	if req.Now.IsZero() {
		req.Now = time.Now()
	}
	switch name {
	case "opencode-go":
		return fetchOpencodeUsage(ctx, req)
	case "commandcode":
		return fetchCommandCodeUsage(ctx, req)
	case "deepseek":
		return fetchDeepSeekUsage(ctx, req)
	default:
		return "", fmt.Errorf("%s: %w", req.Provider, ErrUsageUnsupported)
	}
}

// UsageHint is the explanation printed for a provider without an endpoint. It
// names what is supported and why a relay cannot answer the question.
func UsageHint(providerName string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "usage: %s exposes no quota endpoint.\n", providerName)
	b.WriteString("  supported here: opencode-go, commandcode, deepseek\n")
	b.WriteString("  a relay (sub2api and friends) shows its own wallet balance in its console;")
	b.WriteString(" it does not carry the upstream subscription's remaining quota.")
	return b.String()
}

// usageClientFor builds the client for one lookup. It is a variable so a test can
// substitute a stub transport: the alternative — an httptest server — needs a
// listening socket, which the hermetic test path does not have.
var usageClientFor = func(name, base string) *http.Client {
	timeout := 12 * time.Second
	if c := clientForURL(name, base); c != nil {
		copyClient := *c
		copyClient.Timeout = timeout
		return &copyClient
	}
	return &http.Client{Timeout: timeout}
}

// getJSON fetches url with a bearer token and decodes the body into out.
func getJSON(ctx context.Context, client *http.Client, url, key string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		// The body carries the gateway's own explanation (an expired key, a
		// changed route); a bare status code would send the reader to the docs.
		detail := strings.TrimSpace(string(body))
		if len(detail) > 200 {
			detail = detail[:200]
		}
		if detail != "" {
			return fmt.Errorf("%s: HTTP %d: %s", url, resp.StatusCode, detail)
		}
		return fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%s: unexpected response: %w", url, err)
	}
	return nil
}

// originOf returns scheme://host for an endpoint URL, so a provider whose base URL
// carries a path (/provider/v1) still reaches its billing routes on the host.
func originOf(raw, fallback string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fallback
	}
	return u.Scheme + "://" + u.Host
}

// opencodeUsage is the /zen/go/v1/usage payload.
type opencodeUsage struct {
	Usage struct {
		Rolling usageWindow `json:"rolling"`
		Weekly  usageWindow `json:"weekly"`
		Monthly usageWindow `json:"monthly"`
	} `json:"usage"`
}

// usageWindow is one percentage window: "ok"/"limited" plus the used percentage
// and when it frees up.
type usageWindow struct {
	Status   string    `json:"status"`
	Percent  float64   `json:"percent"`
	ResetsAt time.Time `json:"resetsAt"`
}

func fetchOpencodeUsage(ctx context.Context, req UsageRequest) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
	if base == "" {
		base = "https://opencode.ai/zen/go/v1"
	}
	client := usageClientFor(req.Provider, base)
	var payload opencodeUsage
	if err := getJSON(ctx, client, base+"/usage", req.APIKey, &payload); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("usage · opencode-go\n")
	for _, w := range []struct {
		label string
		win   usageWindow
	}{
		{"5h", payload.Usage.Rolling},
		{"weekly", payload.Usage.Weekly},
		{"monthly", payload.Usage.Monthly},
	} {
		fmt.Fprintf(&b, "  %-8s %3.0f%%   %s\n", w.label, w.win.Percent, resetsIn(req.Now, w.win.ResetsAt))
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// commandCodeCredits is the /alpha/billing/credits payload.
type commandCodeCredits struct {
	Credits struct {
		MonthlyCredits        float64 `json:"monthlyCredits"`
		PurchasedCredits      float64 `json:"purchasedCredits"`
		MonthlyCreditsGranted float64 `json:"monthlyCreditsGranted"`
	} `json:"credits"`
	WindowLimits struct {
		FiveHour commandCodeWindow `json:"fiveHour"`
		Weekly   commandCodeWindow `json:"weekly"`
	} `json:"windowLimits"`
}

// commandCodeWindow is one rolling window in dollars.
type commandCodeWindow struct {
	Used    float64 `json:"used"`
	Cap     float64 `json:"cap"`
	ResetAt int64   `json:"resetAt"`
}

// commandCodeSubscription is the /alpha/billing/subscriptions payload.
type commandCodeSubscription struct {
	Success bool `json:"success"`
	Data    *struct {
		PlanID           string    `json:"planId"`
		Status           string    `json:"status"`
		CurrentPeriodEnd time.Time `json:"currentPeriodEnd"`
	} `json:"data"`
}

func fetchCommandCodeUsage(ctx context.Context, req UsageRequest) (string, error) {
	base := originOf(req.BaseURL, "https://api.commandcode.ai")
	client := usageClientFor(req.Provider, base)

	var credits commandCodeCredits
	if err := getJSON(ctx, client, base+"/alpha/billing/credits", req.APIKey, &credits); err != nil {
		return "", err
	}
	// The subscription lookup only adds the plan name and billing period; a
	// failure there leaves the credits report intact.
	var sub commandCodeSubscription
	_ = getJSON(ctx, client, base+"/alpha/billing/subscriptions", req.APIKey, &sub)

	var b strings.Builder
	b.WriteString("usage · commandcode")
	if sub.Data != nil && sub.Data.PlanID != "" {
		fmt.Fprintf(&b, " (%s", sub.Data.PlanID)
		if sub.Data.Status != "" {
			b.WriteString(" · " + sub.Data.Status)
		}
		b.WriteString(")")
	}
	b.WriteString("\n")
	for _, w := range []struct {
		label string
		win   commandCodeWindow
	}{
		{"5h", credits.WindowLimits.FiveHour},
		{"weekly", credits.WindowLimits.Weekly},
	} {
		pct := 0.0
		if w.win.Cap > 0 {
			pct = w.win.Used / w.win.Cap * 100
		}
		reset := time.Time{}
		if w.win.ResetAt > 0 {
			reset = time.UnixMilli(w.win.ResetAt)
		}
		fmt.Fprintf(&b, "  %-8s $%.2f / $%.2f  (%.1f%%)   %s\n", w.label, w.win.Used, w.win.Cap, pct, resetsIn(req.Now, reset))
	}
	granted := credits.Credits.MonthlyCreditsGranted
	if granted > 0 {
		used := granted - credits.Credits.MonthlyCredits
		pct := used / granted * 100
		fmt.Fprintf(&b, "  monthly  $%.2f left of $%.2f  (%.1f%% used)\n", credits.Credits.MonthlyCredits, granted, pct)
	} else {
		fmt.Fprintf(&b, "  monthly  $%.2f left\n", credits.Credits.MonthlyCredits)
	}
	if sub.Data != nil && !sub.Data.CurrentPeriodEnd.IsZero() {
		fmt.Fprintf(&b, "  renews   %s\n", sub.Data.CurrentPeriodEnd.Local().Format("2006-01-02 15:04"))
	}
	if credits.Credits.PurchasedCredits > 0 {
		fmt.Fprintf(&b, "  top-up   $%.2f\n", credits.Credits.PurchasedCredits)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// deepSeekBalance is the /user/balance payload.
type deepSeekBalance struct {
	Available bool `json:"is_available"`
	Infos     []struct {
		Currency     string `json:"currency"`
		TotalBalance string `json:"total_balance"`
		Granted      string `json:"granted_balance"`
		ToppedUp     string `json:"topped_up_balance"`
	} `json:"balance_infos"`
}

func fetchDeepSeekUsage(ctx context.Context, req UsageRequest) (string, error) {
	base := originOf(req.BaseURL, "https://api.deepseek.com")
	client := usageClientFor(req.Provider, base)
	var bal deepSeekBalance
	if err := getJSON(ctx, client, base+"/user/balance", req.APIKey, &bal); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("usage · deepseek")
	if bal.Available {
		b.WriteString(" (available)")
	} else {
		b.WriteString(" (unavailable)")
	}
	b.WriteString("\n")
	if len(bal.Infos) == 0 {
		b.WriteString("  no balance reported\n")
	}
	for _, info := range bal.Infos {
		fmt.Fprintf(&b, "  balance  %s %s\n", info.Currency, info.TotalBalance)
		if info.ToppedUp != "" && info.ToppedUp != "0.00" {
			fmt.Fprintf(&b, "  topped up %s %s\n", info.Currency, info.ToppedUp)
		}
		if info.Granted != "" && info.Granted != "0.00" {
			fmt.Fprintf(&b, "  granted  %s %s\n", info.Currency, info.Granted)
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// resetsIn renders a reset time relative to now, in the compact form the rest of
// the bar uses. A zero time or one already past reads as "now".
func resetsIn(now, at time.Time) string {
	if at.IsZero() {
		return ""
	}
	d := at.Sub(now)
	if d <= 0 {
		return "resets now"
	}
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("resets in %dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	case d >= time.Hour:
		return fmt.Sprintf("resets in %dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("resets in %dm", int(d.Minutes()))
	}
}
