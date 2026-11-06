package agenttool

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/browserctl"
)

const internalActiveBrowserInfo = `{"apiVersion":"v1","browserId":"browser-1","backend":"native-cdp","activeTabId":"internal","tabs":[` +
	`{"id":"business","targetId":"business-target","url":"https://example.test/reports","index":1},` +
	`{"id":"internal","targetId":"internal-target","url":"chrome://settings/help","index":2,"active":true}]}`

func regressionBrowser(t *testing.T) (*BrowserTool, *fakeBrowser) {
	t.Helper()
	f := &fakeBrowser{info: internalActiveBrowserInfo, replies: map[string]string{}}
	tool := newFakeBrowserToolWith(t, t.TempDir(), f)
	tool.boundSocket, tool.boundBrowser, tool.lastTab = "stub", "browser-1", "business"
	tool.lastTargetID = "business-target"
	return tool, f
}

func TestScreenshotPinsBusinessTabEvenWhenInternalTabIsActive(t *testing.T) {
	tool, f := regressionBrowser(t)
	prepared, err := tool.PrepareCall(context.Background(), json.RawMessage(`{"action":"screenshot","full_page":true}`))
	if err != nil {
		t.Fatal(err)
	}
	var args browserArgs
	if err := json.Unmarshal(prepared, &args); err != nil || args.Tab != "business" || args.TargetID != "business-target" || args.PageURL != "https://example.test/reports" {
		t.Fatalf("prepared screenshot = %+v, %v", args, err)
	}
	result, err := tool.Execute(context.Background(), "screenshot", prepared, nil)
	if err != nil || result.IsError {
		t.Fatalf("screenshot failed: %s (%v)", agentcore.ContentToText(result.Content), err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.actions) != 1 || f.actions[0]["action"] != "screenshot" {
		t.Fatalf("actions = %+v", f.actions)
	}
	target, _ := f.actions[0]["target"].(map[string]any)
	params, _ := f.actions[0]["params"].(map[string]any)
	if target["tabId"] != "business" || target["targetId"] != "business-target" || params["full"] != true {
		t.Fatalf("screenshot targeted %+v, params %+v", target, params)
	}
}

func TestExplicitInternalScreenshotIsRefused(t *testing.T) {
	tool, f := regressionBrowser(t)
	result := runBrowserCall(t, tool, `{"action":"screenshot","tab":"internal"}`)
	if !result.IsError || len(f.actions) != 0 {
		t.Fatalf("internal screenshot was not refused: %+v, actions=%+v", result, f.actions)
	}
}

func TestScreenshotRefusesTargetReplacementAfterReview(t *testing.T) {
	tool, f := regressionBrowser(t)
	prepared, err := tool.PrepareCall(context.Background(), json.RawMessage(`{"action":"screenshot"}`))
	if err != nil {
		t.Fatal(err)
	}
	f.info = strings.Replace(f.info, `"targetId":"business-target"`, `"targetId":"replacement-target"`, 1)
	result, err := tool.Execute(context.Background(), "screenshot", prepared, nil)
	if err != nil || !result.IsError || len(f.actions) != 0 || !strings.Contains(agentcore.ContentToText(result.Content), "target changed") {
		t.Fatalf("replacement target was not refused: %+v, actions=%+v, err=%v", result, f.actions, err)
	}
}

func TestPinnedTabRefusesTargetReplacementBetweenCalls(t *testing.T) {
	tool, f := regressionBrowser(t)
	f.info = strings.Replace(f.info, `"targetId":"business-target"`, `"targetId":"replacement-target"`, 1)
	result := runBrowserCall(t, tool, `{"action":"snapshot"}`)
	if !result.IsError || len(f.actions) != 0 || !strings.Contains(agentcore.ContentToText(result.Content), "replaced") {
		t.Fatalf("replaced pin was not refused: %+v, actions=%+v", result, f.actions)
	}
}

func TestExpectedPageURLCannotBeOverwrittenDuringPreparation(t *testing.T) {
	tool, _ := regressionBrowser(t)
	_, err := tool.PrepareCall(context.Background(), json.RawMessage(`{"action":"snapshot","page_url":"https://other.test/"}`))
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected URL mismatch = %v", err)
	}
}

func TestOnlyOwnedBrowserMayCloseExplicitInternalTab(t *testing.T) {
	for _, owned := range []bool{false, true} {
		tool, f := regressionBrowser(t)
		if owned {
			tool.Control.browsers["browser-1"] = controlledBrowser{socket: canonicalBrowserSocket("stub"), browserRuntime: true, policy: controlPolicyStamp()}
		}
		result := runBrowserCall(t, tool, `{"action":"tab_close","tab":"internal"}`)
		if result.IsError == owned {
			t.Fatalf("owned=%v: %s", owned, agentcore.ContentToText(result.Content))
		}
		if !owned && len(f.actions) != 0 {
			t.Fatalf("sent input to an external internal tab: %+v", f.actions)
		}
		if owned && (len(f.actions) != 1 || f.actions[0]["action"] != "tab.close") {
			t.Fatalf("owned close actions = %+v", f.actions)
		}
	}
}

func TestTabCloseAndSelectRequireExplicitTabBeforeReview(t *testing.T) {
	tool, _ := regressionBrowser(t)
	for _, action := range []string{"tab_close", "tab_select"} {
		raw, _ := json.Marshal(map[string]string{"action": action})
		if _, err := tool.PrepareCall(context.Background(), raw); err == nil {
			t.Fatalf("%s implicitly chose a tab", action)
		}
	}
}

func TestOwnedBrowserCannotCloseLocalFileThroughInternalException(t *testing.T) {
	tool, f := regressionBrowser(t)
	tool.Control.browsers["browser-1"] = controlledBrowser{socket: canonicalBrowserSocket("stub"), browserRuntime: true, policy: controlPolicyStamp()}
	f.info = strings.Replace(f.info, "chrome://settings/help", "file:///etc/passwd", 1)
	result := runBrowserCall(t, tool, `{"action":"tab_close","tab":"internal"}`)
	if !result.IsError || len(f.actions) != 0 {
		t.Fatalf("local file close was not refused: %+v, actions=%+v", result, f.actions)
	}
}

func TestMissingPinnedTabNeverFallsBackToAnotherPage(t *testing.T) {
	tool, f := regressionBrowser(t)
	tool.lastTab = "closed-tab"
	result := runBrowserCall(t, tool, `{"action":"snapshot"}`)
	if !result.IsError || len(f.actions) != 0 || !strings.Contains(agentcore.ContentToText(result.Content), "closed-tab") {
		t.Fatalf("missing pinned tab silently fell back: %+v, actions=%+v", result, f.actions)
	}
}

func TestDefaultTabSelectionRejectsAmbiguityButFindsLaterActiveTab(t *testing.T) {
	info := browserctl.Info{BrowserID: "b", ActiveTabID: "internal", Tabs: []browserctl.Tab{
		{ID: "one", TargetID: "t1", URL: "https://one.test"},
		{ID: "two", TargetID: "t2", URL: "https://two.test"},
		{ID: "internal", TargetID: "t3", URL: "chrome://settings/help", Active: true},
	}}
	if _, err := pinTab(info, ""); err == nil {
		t.Fatal("ambiguous safe tabs were selected silently")
	}
	info.ActiveTabID, info.Tabs[2].Active = "two", false
	info.Tabs[1].Active = true
	if target, err := pinTab(info, ""); err != nil || target.TabID != "two" {
		t.Fatalf("active safe tab = %+v, %v", target, err)
	}
}

func TestExplicitTabSelectionUpdatesPinnedTarget(t *testing.T) {
	tool, f := regressionBrowser(t)
	f.info = strings.Replace(f.info, "chrome://settings/help", "https://other.test/", 1)
	result := runBrowserCall(t, tool, `{"action":"tab_select","tab":"internal"}`)
	if result.IsError || tool.lastTab != "internal" || tool.lastTargetID != "internal-target" {
		t.Fatalf("tab selection did not update pin: result=%s tab=%q target=%q", agentcore.ContentToText(result.Content), tool.lastTab, tool.lastTargetID)
	}
	details, ok := result.Details.(map[string]any)
	if !ok || details["action"] != "tab.activate" || details["browser"] != "browser-1" || details["tab"] != "internal" || details["target_id"] != "internal-target" || details["socket"] != "stub" {
		t.Fatalf("selection did not expose exact view identity: %#v", result.Details)
	}
	result = runBrowserCall(t, tool, `{"action":"snapshot"}`)
	if result.IsError || len(f.actions) != 2 {
		t.Fatalf("snapshot after selection failed: %s, actions=%+v", agentcore.ContentToText(result.Content), f.actions)
	}
	target, _ := f.actions[1]["target"].(map[string]any)
	if target["tabId"] != "internal" || target["targetId"] != "internal-target" {
		t.Fatalf("snapshot ignored new pin: %+v", target)
	}
}

func TestTabSelectionIncludesOnlyMatchingHostRevision(t *testing.T) {
	for _, matches := range []bool{false, true} {
		tool, f := regressionBrowser(t)
		f.info = strings.Replace(f.info, "chrome://settings/help", "https://other.test/", 1)
		browserID := "browser-1"
		if !matches {
			browserID = "different-browser"
		}
		f.replies["tab.activate"] = `{"apiVersion":"v1","browserId":"` + browserID + `","explicitSelection":{"revision":42,"tabId":"internal","targetId":"internal-target"}}`
		result := runBrowserCall(t, tool, `{"action":"tab_select","tab":"internal"}`)
		if result.IsError {
			t.Fatalf("selection failed: %s", agentcore.ContentToText(result.Content))
		}
		details, _ := result.Details.(map[string]any)
		revision, exists := details["selection_revision"]
		if exists != matches || matches && revision != "42" {
			t.Fatalf("matches=%v revision=%v exists=%v", matches, revision, exists)
		}
	}
}

func TestNewTabPinsOnlyAConfirmedSafeTarget(t *testing.T) {
	for _, valid := range []bool{false, true} {
		tool, _ := regressionBrowser(t)
		posts := 0
		tool.newClient = func() (*browserctl.Client, error) {
			return browserctl.NewWithTransport("stub", startupRoundTrip(func(r *http.Request) (*http.Response, error) {
				body := internalActiveBrowserInfo
				if r.Method == http.MethodPost {
					posts++
					meta := `{"browserId":"browser-1","tabId":"new","targetId":"new-target","url":"https://new.test/"}`
					if !valid {
						meta = `{"browserId":"browser-1","tabId":"new","url":"https://new.test/"}`
					}
					body = `{"ok":true,"result":{},"meta":` + meta + `}`
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})), nil
		}
		result := runBrowserCall(t, tool, `{"action":"tab_new","url":"https://new.test/"}`)
		if result.IsError == valid || posts != 1 {
			t.Fatalf("valid=%v result=%s posts=%d", valid, agentcore.ContentToText(result.Content), posts)
		}
		if valid && (tool.lastTab != "new" || tool.lastTargetID != "new-target") {
			t.Fatalf("new tab not pinned: %q %q", tool.lastTab, tool.lastTargetID)
		}
		if !valid && (tool.lastTab != "business" || tool.lastTargetID != "business-target") {
			t.Fatal("unconfirmed tab replaced the existing pin")
		}
	}
}

func TestTabSelectRejectsMismatchedResponse(t *testing.T) {
	tool, _ := regressionBrowser(t)
	tool.newClient = func() (*browserctl.Client, error) {
		return browserctl.NewWithTransport("stub", startupRoundTrip(func(r *http.Request) (*http.Response, error) {
			body := strings.Replace(internalActiveBrowserInfo, "chrome://settings/help", "https://other.test/", 1)
			if r.Method == http.MethodPost {
				body = `{"ok":true,"result":{},"meta":{"browserId":"browser-1","tabId":"internal","targetId":"wrong","url":"https://other.test/"}}`
			}
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		})), nil
	}
	result := runBrowserCall(t, tool, `{"action":"tab_select","tab":"internal"}`)
	if !result.IsError || tool.lastTab != "business" || tool.lastTargetID != "business-target" {
		t.Fatalf("mismatched selection response replaced pin: %s, tab=%q target=%q", agentcore.ContentToText(result.Content), tool.lastTab, tool.lastTargetID)
	}
}

func TestDuplicateURLPrefersExactPinOrFailsClosed(t *testing.T) {
	info := browserctl.Info{Tabs: []browserctl.Tab{
		{ID: "one", TargetID: "t1", URL: "https://example.test/reports"},
		{ID: "two", TargetID: "t2", URL: "https://example.test/reports"},
	}}
	if _, _, err := matchingBrowserTab(info, "https://example.test/reports", "", ""); err == nil {
		t.Fatal("duplicate URL silently selected a tab")
	}
	if tab, ok, err := matchingBrowserTab(info, "https://example.test/reports", "two", "t2"); err != nil || !ok || tab.ID != "two" {
		t.Fatalf("exact pin = %+v, %v, %v", tab, ok, err)
	}
	if _, _, err := matchingBrowserTab(info, "https://example.test/reports", "two", "old-target"); err == nil {
		t.Fatal("replacement target was accepted as the old pin")
	}
}

func TestStartupTargetMatchingPreservesMeaningfulURLParts(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"https://example.test", "https://example.test/", true},
		{"https://example.test/reports", "https://example.test/reports/", false},
		{"https://example.test/?path=/", "https://example.test/?path=", false},
		{"https://example.test/#one", "https://example.test/#two", false},
	} {
		if got := sameBrowserURL(tc.a, tc.b); got != tc.want {
			t.Errorf("sameBrowserURL(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
	info := browserctl.Info{Tabs: []browserctl.Tab{
		{ID: "initial", TargetID: "t1", URL: "about:blank", Index: 1},
		{ID: "restored", TargetID: "t2", URL: "https://old.test/", Index: 2},
	}}
	if tab, ok := initialBrowserTab(info, "https://requested.test/"); ok {
		t.Fatalf("restored page mistaken for redirect: %+v", tab)
	}
	info.Tabs[0].URL = "https://login.test/"
	info.Tabs = info.Tabs[:1]
	if tab, ok := initialBrowserTab(info, "https://requested.test/"); !ok || tab.ID != "initial" {
		t.Fatalf("unambiguous initial redirect was not selected: %+v, %v", tab, ok)
	}
}

func TestRebindingSameBrowserPreservesPinnedTab(t *testing.T) {
	tool, _ := regressionBrowser(t)
	tool.bind(browserInstance{Socket: "stub", Info: browserctl.Info{BrowserID: "browser-1"}})
	if tool.lastTab != "business" {
		t.Fatalf("same-browser open lost pin: %q", tool.lastTab)
	}
	tool.bind(browserInstance{Socket: "other", Info: browserctl.Info{BrowserID: "browser-2"}})
	if tool.lastTab != "" {
		t.Fatalf("new browser retained old pin: %q", tool.lastTab)
	}
}

type startupRoundTrip func(*http.Request) (*http.Response, error)

func (f startupRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestStartupNeverRetriesPausedActivation(t *testing.T) {
	posts := 0
	client := browserctl.NewWithTransport("stub", startupRoundTrip(func(r *http.Request) (*http.Response, error) {
		body := internalActiveBrowserInfo
		if r.Method == http.MethodPost {
			posts++
			body = `{"ok":false,"error":{"code":"browser_paused","message":"operator needed","retryable":false}}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	}))
	tool := NewBrowserTool(t.TempDir(), true)
	err := tool.selectOpenedTabWithClient(context.Background(), client, "https://example.test/reports")
	if err == nil || !strings.Contains(err.Error(), "browser_paused") || posts != 1 || tool.lastTab != "" {
		t.Fatalf("activation err=%v posts=%d pinned=%q", err, posts, tool.lastTab)
	}
}

func TestActivationRejectsWrongResponseIdentity(t *testing.T) {
	client := browserctl.NewWithTransport("stub", startupRoundTrip(func(*http.Request) (*http.Response, error) {
		body := `{"ok":true,"result":{},"meta":{"browserId":"browser-1","tabId":"business","targetId":"wrong","url":"https://example.test/reports"}}`
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	}))
	info := browserctl.Info{BrowserID: "browser-1", ActiveTabID: "other"}
	tab := browserctl.Tab{ID: "business", TargetID: "business-target", URL: "https://example.test/reports"}
	if err := activateBrowserTab(context.Background(), client, info, tab); err == nil {
		t.Fatal("activation accepted a mismatched target response")
	}
}
