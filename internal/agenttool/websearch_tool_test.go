// Tests for the websearch tool: backend auto-selection by credential, per-backend
// response parsing (Tavily JSON, Exa JSON with highlights, DuckDuckGo HTML
// with redirect-wrapped URLs), domain filtering, count clamping, and structured
// errors. A fake RoundTripper serves canned responses so no network is touched.
package agenttool

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

// execWebSearch runs the tool with a fake transport and a fixed environment.
func execWebSearch(t *testing.T, env map[string]string, fn roundTripFunc, args string) agentcore.AgentToolResult {
	t.Helper()
	tool := &WebSearchTool{
		Client: &http.Client{Transport: fn},
		getenv: func(k string) string { return env[k] },
	}
	res, err := tool.Execute(context.Background(), "c1", json.RawMessage(args), nil)
	if err != nil {
		t.Fatalf("Execute returned Go error: %v", err)
	}
	return res
}

// resultText is defined in read_tool_test.go (same package).

func TestSelectSearchBackend(t *testing.T) {
	cases := []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"TAVILY_API_KEY": "t"}, "tavily"},
		{map[string]string{"EXA_API_KEY": "e"}, "exa"},
		{map[string]string{"TAVILY_API_KEY": "t", "EXA_API_KEY": "e"}, "tavily"},
		{map[string]string{}, "duckduckgo"},
	}
	for _, c := range cases {
		got := selectSearchBackend(func(k string) string { return c.env[k] }).name()
		if got != c.want {
			t.Errorf("env %v: backend = %q, want %q", c.env, got, c.want)
		}
	}
}

func TestWebSearchTavily(t *testing.T) {
	body := `{"results":[{"title":"Go","url":"https://go.dev","content":"The Go language"},{"title":"Docs","url":"https://pkg.go.dev","content":"packages"}]}`
	var gotAuth string
	res := execWebSearch(t, map[string]string{"TAVILY_API_KEY": "secret"},
		func(r *http.Request) (*http.Response, error) {
			if r.URL.Host != "api.tavily.com" {
				t.Errorf("unexpected host %q", r.URL.Host)
			}
			gotAuth = r.Header.Get("Authorization")
			return makeResp(200, "application/json", body), nil
		}, `{"query":"go language"}`)

	txt := resultText(res)
	if gotAuth != "Bearer secret" {
		t.Errorf("Authorization = %q, want Bearer secret", gotAuth)
	}
	if !strings.Contains(txt, "via tavily") || !strings.Contains(txt, "https://go.dev") || !strings.Contains(txt, "The Go language") {
		t.Errorf("unexpected result:\n%s", txt)
	}
	if bk, _ := res.Details.(map[string]any)["backend"].(string); bk != "tavily" {
		t.Errorf("Details.backend = %q, want tavily", bk)
	}
}

func TestWebSearchExaHighlights(t *testing.T) {
	body := `{"requestId":"r1","resolvedSearchType":"neural","results":[` +
		`{"title":"Rust","url":"https://rust-lang.org","highlights":["A systems language","with memory safety"]},` +
		`{"title":"Docs","url":"https://doc.rust-lang.org","highlights":[]}]}`
	var gotKey, gotBody string
	res := execWebSearch(t, map[string]string{"EXA_API_KEY": "tok"},
		func(r *http.Request) (*http.Response, error) {
			if r.URL.Host != "api.exa.ai" || r.URL.Path != "/search" {
				t.Errorf("unexpected URL %q", r.URL.String())
			}
			gotKey = r.Header.Get("x-api-key")
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
			return makeResp(200, "application/json", body), nil
		}, `{"query":"rust"}`)

	txt := resultText(res)
	if gotKey != "tok" {
		t.Errorf("x-api-key = %q, want tok", gotKey)
	}
	// The request enables highlights, matching codex's Exa request shape.
	for _, want := range []string{`"contents":{"highlights":true}`, `"type":"auto"`} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("request body missing %s: %s", want, gotBody)
		}
	}
	if !strings.Contains(txt, "via exa") || !strings.Contains(txt, "https://rust-lang.org") {
		t.Errorf("unexpected result:\n%s", txt)
	}
	if !strings.Contains(txt, "A systems language") || !strings.Contains(txt, "with memory safety") {
		t.Errorf("highlights should be joined into the snippet:\n%s", txt)
	}
	if bk, _ := res.Details.(map[string]any)["backend"].(string); bk != "exa" {
		t.Errorf("Details.backend = %q, want exa", bk)
	}
}

func TestWebSearchDuckDuckGo(t *testing.T) {
	html := `<div class="result">
      <a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fa&rut=x">First Title</a>
      <a class="result__snippet">First snippet</a>
    </div>
    <div class="result">
      <a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.org%2Fb">Second Title</a>
      <a class="result__snippet">Second snippet</a>
    </div>`
	res := execWebSearch(t, map[string]string{},
		func(r *http.Request) (*http.Response, error) {
			if r.URL.Host != "html.duckduckgo.com" {
				t.Errorf("unexpected host %q", r.URL.Host)
			}
			return makeResp(200, "text/html", html), nil
		}, `{"query":"anything"}`)

	txt := resultText(res)
	if !strings.Contains(txt, "https://example.com/a") || !strings.Contains(txt, "https://example.org/b") {
		t.Errorf("redirect URLs not decoded:\n%s", txt)
	}
	if !strings.Contains(txt, "First Title") || !strings.Contains(txt, "Second snippet") {
		t.Errorf("titles/snippets missing:\n%s", txt)
	}
}

func TestWebSearchDomainFilter(t *testing.T) {
	body := `{"results":[{"title":"A","url":"https://keep.com/x","content":"a"},{"title":"B","url":"https://drop.com/y","content":"b"}]}`
	res := execWebSearch(t, map[string]string{"TAVILY_API_KEY": "k"},
		func(r *http.Request) (*http.Response, error) {
			return makeResp(200, "application/json", body), nil
		}, `{"query":"q","allowed_domains":["keep.com"]}`)

	txt := resultText(res)
	if strings.Contains(txt, "drop.com") {
		t.Errorf("blocked domain leaked:\n%s", txt)
	}
	if !strings.Contains(txt, "keep.com") {
		t.Errorf("allowed domain dropped:\n%s", txt)
	}
}

func TestWebSearchEmptyQuery(t *testing.T) {
	res := execWebSearch(t, map[string]string{},
		func(r *http.Request) (*http.Response, error) {
			t.Error("transport should not be called for empty query")
			return makeResp(200, "text/html", ""), nil
		}, `{"query":"   "}`)
	if !strings.Contains(resultText(res), "query is required") {
		t.Errorf("want query-required error, got:\n%s", resultText(res))
	}
}

func TestWebSearchBackendError(t *testing.T) {
	res := execWebSearch(t, map[string]string{"TAVILY_API_KEY": "k"},
		func(r *http.Request) (*http.Response, error) {
			return makeResp(500, "application/json", "boom"), nil
		}, `{"query":"q"}`)
	if !strings.Contains(resultText(res), "tavily backend failed") {
		t.Errorf("want backend-failed error, got:\n%s", resultText(res))
	}
}
