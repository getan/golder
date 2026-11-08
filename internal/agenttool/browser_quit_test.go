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
	"github.com/getan/golder/internal/permissions"
)

type quitBrowserTransport struct {
	*fakeBrowser
	quitCalls int
	quitErr   error
}

func (f *quitBrowserTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == http.MethodPost && r.URL.Path == "/v1/quit" {
		f.quitCalls++
		if f.quitErr != nil {
			return nil, f.quitErr
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"apiVersion":"v1","ok":true,"result":{"quitting":true}}`))}, nil
	}
	return f.fakeBrowser.RoundTrip(r)
}

func quitToolFixture(t *testing.T, info string) (*BrowserTool, *quitBrowserTransport) {
	t.Helper()
	f := &quitBrowserTransport{fakeBrowser: &fakeBrowser{info: info}}
	tool := NewBrowserTool(t.TempDir(), true)
	tool.boundSocket, tool.boundBrowser = "stub", "browser-1"
	tool.Control.Mode.Set(permissions.Auto)
	tool.Control.browsers["browser-1"] = controlledBrowser{socket: canonicalBrowserSocket("stub"), isolated: true, policy: controlPolicyStamp()}
	tool.newClient = func() (*browserctl.Client, error) { return browserctl.NewWithTransport("stub", f), nil }
	return tool, f
}

const quitBrowserInfo = `{"apiVersion":"v1","browserId":"browser-1","backend":"native-cdp","tabs":[],"capabilities":[{"name":"quit"}]}`

func TestBrowserQuitPinsIdentityAndRequestsGracefulShutdown(t *testing.T) {
	tool, f := quitToolFixture(t, quitBrowserInfo)
	raw, err := tool.PrepareCall(context.Background(), json.RawMessage(`{"action":"quit","socket":"forged"}`))
	if err != nil || !strings.Contains(string(raw), `"browser":"browser-1"`) || !strings.Contains(string(raw), `"socket":"stub"`) || strings.Contains(string(raw), `"tab"`) {
		t.Fatalf("prepare quit = %s, %v", raw, err)
	}
	res, err := tool.Execute(context.Background(), "quit", raw, nil)
	text := agentcore.ContentToText(res.Content)
	if err != nil || res.IsError || f.quitCalls != 1 || !strings.Contains(text, `"quitting":true`) || !strings.Contains(text, `"exit_confirmed":false`) {
		t.Fatalf("quit = %q, err=%v, calls=%d", text, err, f.quitCalls)
	}
}

func TestBrowserQuitRechecksReviewedSocket(t *testing.T) {
	tool, f := quitToolFixture(t, quitBrowserInfo)
	cfg := newExecCfg(t, tool)
	cfg.BeforeToolCall = func(_ context.Context, call agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		var args browserArgs
		if err := json.Unmarshal(call.Arguments, &args); err != nil || args.Browser != "browser-1" || args.Socket != "stub" {
			t.Fatalf("review saw an unpinned browser: %+v, %v", args, err)
		}
		tool.bind(browserInstance{Socket: "replacement", Info: browserctl.Info{BrowserID: "browser-1"}})
		return nil
	}
	res, _ := executeToolCall(context.Background(), cfg, agentcore.AgentToolCall{ID: "quit", Name: "browser", Arguments: json.RawMessage(`{"action":"quit"}`)}, nil)
	if !res.IsError || f.quitCalls != 0 {
		t.Fatalf("changed socket was used: result=%+v calls=%d", res, f.quitCalls)
	}
}

func TestBrowserQuitRefusesUnreviewedOrChangedTargets(t *testing.T) {
	for _, tc := range []struct {
		name, info, raw string
	}{
		{"no identity", quitBrowserInfo, `{"action":"quit"}`},
		{"different identity", quitBrowserInfo, `{"action":"quit","browser":"other","socket":"stub"}`},
		{"different socket", quitBrowserInfo, `{"action":"quit","browser":"browser-1","socket":"other"}`},
		{"no capability", `{"apiVersion":"v1","browserId":"browser-1","backend":"native-cdp","tabs":[]}`, `{"action":"quit","browser":"browser-1","socket":"stub"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tool, f := quitToolFixture(t, tc.info)
			res, _ := tool.Execute(context.Background(), "quit", json.RawMessage(tc.raw), nil)
			if !res.IsError || f.quitCalls != 0 {
				t.Fatalf("unsafe quit = %+v, calls=%d", res, f.quitCalls)
			}
		})
	}
}

func TestBrowserQuitRespectsControlBoundaries(t *testing.T) {
	tool, f := quitToolFixture(t, quitBrowserInfo)
	delete(tool.Control.browsers, "browser-1")
	raw := json.RawMessage(`{"action":"quit","browser":"browser-1","socket":"stub","host_control":true}`)
	for _, ctx := range []context.Context{context.Background(), agentcore.WithSandboxRequest(context.Background())} {
		res, _ := tool.Execute(ctx, "quit", raw, nil)
		if !res.IsError || f.quitCalls != 0 {
			t.Fatal("external browser quit bypassed host authorization")
		}
	}
	ctx := agentcore.WithHostControlGrant(context.Background())
	res, _ := tool.Execute(ctx, "quit", raw, nil)
	if res.IsError || f.quitCalls != 1 {
		t.Fatalf("approved external quit = %+v, calls=%d", res, f.quitCalls)
	}
	tool.Control.Mode.Set(permissions.ReadOnly)
	res, _ = tool.Execute(ctx, "quit", raw, nil)
	if !res.IsError || f.quitCalls != 1 {
		t.Fatal("read-only permission mode allowed quit")
	}
	tool.allowControl = false
	res, _ = tool.Execute(ctx, "quit", raw, nil)
	if !res.IsError || f.quitCalls != 1 || strings.Contains(string(tool.ValidationSchema()), `"quit"`) {
		t.Fatal("read-only sub-agent was allowed to quit")
	}
}

func TestBrowserQuitDoesNotRetryUnknownOutcome(t *testing.T) {
	tool, f := quitToolFixture(t, quitBrowserInfo)
	f.quitErr = io.EOF
	res, _ := tool.Execute(context.Background(), "quit", json.RawMessage(`{"action":"quit","browser":"browser-1","socket":"stub"}`), nil)
	if !res.IsError || f.quitCalls != 1 || !strings.Contains(agentcore.ContentToText(res.Content), "may have been dispatched") {
		t.Fatalf("quit error = %+v, calls=%d", res, f.quitCalls)
	}
}
