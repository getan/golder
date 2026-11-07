package extensions

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
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

	"github.com/getan/golder/internal/mcpauth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPExplicitOAuthConnectAndRestart(t *testing.T) {
	var calls atomic.Int64
	mcpServer := fixtureServer(&calls)
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, &mcp.StreamableHTTPOptions{Stateless: true})
	mux := http.NewServeMux()
	host := httptest.NewServer(mux)
	defer host.Close()
	resource := host.URL + "/mcp"
	var mu sync.Mutex
	challenge := ""
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-access" {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer %s="%s"`, "resource_"+"metadata", host.URL+"/.well-known/oauth-protected-resource/mcp"))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mcpHandler.ServeHTTP(w, r)
	})
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"resource": resource, "authorization_servers": []string{host.URL}, "scopes_supported": []string{"read"}})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": host.URL, "authorization_endpoint": host.URL + "/authorize", "token_endpoint": host.URL + "/token", "registration_endpoint": host.URL + "/register",
			"response_types_supported": []string{"code"}, "code_challenge_methods_supported": []string{"S256"}, "token_endpoint_auth_methods_supported": []string{"none"},
		})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"client_id": "fixture-client", "redirect_uris": body["redirect_uris"], "token_endpoint_auth_method": "none"})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		mu.Lock()
		challenge = q.Get("code_challenge")
		mu.Unlock()
		u, err := url.Parse(q.Get("redirect_uri"))
		if err != nil || q.Get("code_challenge_method") != "S256" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		values := u.Query()
		values.Set("code", "fixture-code")
		values.Set("state", q.Get("state"))
		u.RawQuery = values.Encode()
		http.Redirect(w, r, u.String(), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		hash := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		mu.Lock()
		valid := challenge != "" && challenge == base64.RawURLEncoding.EncodeToString(hash[:])
		mu.Unlock()
		if !valid || r.Form.Get("code") != "fixture-code" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"fixture-access","token_type":"Bearer","expires_in":3600,"refresh_token":"fixture-refresh"}`)
	})
	ref := SourceRef{Kind: "mcp", ID: "oauth"}
	manager := testManager(t, ref, SourceConfig{Enabled: true, Transport: "http", URL: resource}, nil)
	defer manager.Close() // close long-lived MCP streams before the HTTP server
	if err := manager.Approve(ref); err != nil {
		t.Fatal(err)
	}
	if err := manager.Enable(ref); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := manager.Connect(ctx, ref); err == nil {
		t.Fatal("ordinary connection unexpectedly authorized")
	}
	opened := false
	err := manager.AuthorizeMCP(ctx, ref, func(ctx context.Context, review mcpauth.Review, startURL string) (func(), error) {
		opened = true
		if review.Host == "" || !strings.Contains(strings.Join(review.Scopes, " "), "read") {
			t.Error("operator did not receive the authorization review")
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
	if err != nil || !opened {
		t.Fatalf("explicit authorization failed: %v", err)
	}
	view, ok := manager.Source(ref)
	if !ok || view.State != "ready" || view.Authentication != "verified" || len(view.Tools) == 0 {
		t.Fatalf("unexpected authorized source: %+v", view)
	}
	if calls.Load() != 0 {
		t.Fatal("authorization executed a business tool")
	}
	if err := manager.Disconnect(ref); err != nil {
		t.Fatal(err)
	}
	if err := manager.Connect(ctx, ref); err != nil {
		t.Fatalf("stored noninteractive OAuth reconnect failed: %v", err)
	}
	other, err := New(Options{ConfigPath: manager.opts.ConfigPath, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := other.Approve(ref); err != nil {
		t.Fatal(err)
	}
	if err := other.Enable(ref); err != nil {
		t.Fatal(err)
	}
	if err := other.Connect(ctx, ref); err != nil {
		t.Fatalf("globally configured source did not reuse OAuth grant in another project: %v", err)
	}
}
