package mcpauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestOAuthDynamicRegistrationAndStoredToken(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()
	resource := server.URL + "/mcp"
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"resource": resource, "authorization_servers": []string{server.URL}, "scopes_supported": []string{"read"}})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": server.URL, "authorization_endpoint": server.URL + "/authorize", "token_endpoint": server.URL + "/token",
			"registration_endpoint": server.URL + "/register", "response_types_supported": []string{"code"},
			"code_challenge_methods_supported": []string{"S256"}, "token_endpoint_auth_methods_supported": []string{"none"},
		})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("invalid registration")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"client_id": "test-client", "redirect_uris": request["redirect_uris"], "token_endpoint_auth_method": "none"})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("state") == "" {
			t.Error("authorization request omitted state or PKCE")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		callback, err := url.Parse(q.Get("redirect_uri"))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		values := callback.Query()
		values.Set("state", q.Get("state"))
		values.Set("code", "test-code")
		callback.RawQuery = values.Encode()
		http.Redirect(w, r, callback.String(), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.Form.Get("code_verifier") == "" || r.Form.Get("code") != "test-code" {
			t.Error("token exchange omitted code or PKCE verifier")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"fixture-access","token_type":"Bearer","expires_in":3600,"refresh_token":"fixture-refresh"}`)
	})
	opened := false
	flow, err := New("mcp:test", "reviewed-fingerprint", resource, func(ctx context.Context, review Review, startURL string) (func(), error) {
		opened = true
		if review.Host == "" || !strings.Contains(strings.Join(review.Scopes, " "), "read") {
			t.Error("operator review omitted host or scope")
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, startURL, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("callback status %d", resp.StatusCode)
		}
		return func() {}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	req, _ := http.NewRequest(http.MethodPost, resource, nil)
	resp := &http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{}, Body: http.NoBody, Request: req}
	resp.Header.Set("WWW-Authenticate", `Bearer resource_metadata="`+server.URL+`/.well-known/oauth-protected-resource/mcp"`)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := flow.handler.Authorize(ctx, req, resp); err != nil {
		t.Fatal(err)
	}
	if !opened {
		t.Fatal("operator authorization browser was not requested")
	}
	handler, err := Stored("mcp:test", "reviewed-fingerprint", resource)
	if err != nil || handler == nil {
		t.Fatalf("stored grant unavailable: %v", err)
	}
	source, err := handler.TokenSource(ctx)
	if err != nil {
		t.Fatal(err)
	}
	token, err := source.Token()
	if err != nil || token.AccessToken != "fixture-access" {
		t.Fatal("saved OAuth token was not recovered")
	}
	other, err := Stored("mcp:test", "different-fingerprint", resource)
	if err != nil || other != nil {
		t.Fatal("different source fingerprint reused the grant")
	}
}

func TestCallbackRejectsStateMismatchAndReplay(t *testing.T) {
	flow, err := New("mcp:test", "fingerprint", "http://127.0.0.1:12345/mcp", func(context.Context, Review, string) (func(), error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	flow.state = "expected"
	makeRequest := func(query string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "http://"+flow.listener.Addr().String()+"/callback?"+query, nil)
		w := httptest.NewRecorder()
		flow.callback(w, r)
		return w
	}
	if got := makeRequest("state=wrong&code=one").Code; got != http.StatusBadRequest {
		t.Fatalf("mismatched state accepted: %d", got)
	}
	if got := makeRequest("state=expected&code=one").Code; got != http.StatusOK {
		t.Fatalf("valid callback rejected: %d", got)
	}
	if got := makeRequest("state=expected&code=two").Code; got != http.StatusBadRequest {
		t.Fatalf("callback replay accepted: %d", got)
	}
}

func TestOAuthNetworkRejectsInsecureDestinations(t *testing.T) {
	client, err := safeClient("https://example.com/mcp")
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"http://127.0.0.1/private", "http://169.254.169.254/latest", "http://example.com/token"} {
		req, _ := http.NewRequest(http.MethodGet, endpoint, nil)
		if _, err := client.Do(req); err == nil {
			t.Fatalf("allowed insecure OAuth endpoint %s", endpoint)
		}
	}
}

func TestOAuthPreRegisteredPublicClientAndIssuer(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()
	resource := server.URL + "/mcp"
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"resource": resource, "authorization_servers": []string{server.URL}})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": server.URL, "authorization_endpoint": server.URL + "/authorize", "token_endpoint": server.URL + "/token",
			"response_types_supported": []string{"code"}, "code_challenge_methods_supported": []string{"S256"}, "token_endpoint_auth_methods_supported": []string{"none"},
		})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("client_id") != "public-client" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		callback, _ := url.Parse(q.Get("redirect_uri"))
		values := callback.Query()
		values.Set("state", q.Get("state"))
		values.Set("code", "fixture-code")
		callback.RawQuery = values.Encode()
		http.Redirect(w, r, callback.String(), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("code") != "fixture-code" || r.Form.Get("code_verifier") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"fixture-access","token_type":"Bearer","expires_in":3600}`)
	})
	opened := false
	open := func(ctx context.Context, _ Review, start string) (func(), error) {
		opened = true
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, start, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("callback failed")
		}
		return func() {}, nil
	}
	newResponse := func(req *http.Request) *http.Response {
		resp := &http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{}, Body: http.NoBody, Request: req}
		resp.Header.Set("WWW-Authenticate", fmt.Sprintf(`Bearer %s="%s"`, "resource_"+"metadata", server.URL+"/.well-known/oauth-protected-resource/mcp"))
		return resp
	}
	req, _ := http.NewRequest(http.MethodPost, resource, nil)
	wrong, err := NewWithClient("mcp:test", "wrong", resource, Registration{ClientID: "public-client", Issuer: server.URL + "/wrong"}, open)
	if err != nil {
		t.Fatal(err)
	}
	if err := wrong.Authorize(context.Background(), req, newResponse(req)); err == nil || opened {
		t.Fatal("a mismatched issuer was accepted or opened a browser")
	}
	wrong.Close()
	flow, err := NewWithClient("mcp:test", "correct", resource, Registration{ClientID: "public-client", Issuer: server.URL}, open)
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	if err := flow.Authorize(context.Background(), req, newResponse(req)); err != nil || !opened {
		t.Fatalf("pre-registered public client failed: %v", err)
	}
}

func TestConcurrentOAuthRefreshUsesRotatedGrant(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	var refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "old-refresh" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		refreshes.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"new-access","refresh_token":"new-refresh","token_type":"Bearer","expires_in":3600}`)
	}))
	defer server.Close()
	resource := server.URL + "/mcp"
	client, err := safeClient(resource)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, client)
	cfg := &oauth2.Config{ClientID: "fixture", Endpoint: oauth2.Endpoint{TokenURL: server.URL + "/token", AuthStyle: oauth2.AuthStyleInParams}}
	old := &oauth2.Token{AccessToken: "old-access", RefreshToken: "old-refresh", Expiry: time.Now().Add(-time.Minute)}
	if _, err := makeTokenSource(ctx, credentialName("mcp:test", "same"), cfg, old, nil); err != nil {
		t.Fatal(err)
	}
	one, err := Stored("mcp:test", "same", resource)
	if err != nil || one == nil {
		t.Fatalf("first stored grant: %v", err)
	}
	two, err := Stored("mcp:test", "same", resource)
	if err != nil || two == nil {
		t.Fatalf("second stored grant: %v", err)
	}
	sources := make([]oauth2.TokenSource, 0, 2)
	for _, handler := range []interface {
		TokenSource(context.Context) (oauth2.TokenSource, error)
	}{one, two} {
		ts, err := handler.TokenSource(ctx)
		if err != nil {
			t.Fatal(err)
		}
		sources = append(sources, ts)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, source := range sources {
		wg.Add(1)
		go func(source oauth2.TokenSource) {
			defer wg.Done()
			token, err := source.Token()
			if err == nil && token.AccessToken != "new-access" {
				err = fmt.Errorf("unexpected refreshed token")
			}
			results <- err
		}(source)
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("concurrent refresh failed: %v", err)
		}
	}
	if got := refreshes.Load(); got != 1 {
		t.Fatalf("refresh endpoint called %d times, want one", got)
	}
}
