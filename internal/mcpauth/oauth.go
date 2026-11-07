// Package mcpauth implements opt-in, source-scoped MCP OAuth transactions.
package mcpauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/getan/golder/internal/filelock"
	"github.com/getan/golder/internal/provider"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/net/idna"
	"golang.org/x/oauth2"
)

var (
	ErrBrowserUnavailable  = errors.New("private authorization browser is unavailable; a visible Golder workspace or supported GUI is required")
	ErrAuthorizationDenied = errors.New("authorization was denied by the provider")
)

// Review contains only the authorization domain and requested scope. The
// complete authorization URL and state stay in the private transaction.
type Review struct {
	Host   string
	Scopes []string
}

// Open is an operator-only browser launcher. It must ask the person to review
// the host/scopes, then open startURL in an isolated browser. startURL points to
// a random loopback trampoline, not the authorization endpoint or a secret.
type Open func(ctx context.Context, review Review, startURL string) (close func(), err error)

type storedGrant struct {
	Config oauth2.Config `json:"config"`
	Token  oauth2.Token  `json:"token"`
}

// credentialName binds authorization to the reviewed source fingerprint; a
// changed URL, command, headers or other capability cannot reuse an old grant.
func credentialName(source, fingerprint string) string {
	sum := sha256.Sum256([]byte(source + "\x00" + fingerprint))
	return "mcp-oauth-" + hex.EncodeToString(sum[:20])
}

type tokenSource struct {
	mu      sync.Mutex
	name    string
	config  oauth2.Config
	token   oauth2.Token
	base    oauth2.TokenSource
	ctx     context.Context
	observe func(...string)
}

func (s *tokenSource) Token() (*oauth2.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.token.Valid() {
		// Refresh-token rotation must serialize across Golder processes. Reload
		// the latest persisted grant under a per-source lock before refreshing.
		path := provider.CredentialFilePath()
		if path == "" {
			return nil, errors.New("OAuth credentials are unavailable; authorize again")
		}
		unlock, err := filelock.Acquire(path + ".oauth-refresh-" + s.name)
		if err != nil {
			return nil, errors.New("cannot lock OAuth credentials for refresh")
		}
		defer unlock()
		raw, err := provider.ResolveCredentialReference(path, s.name)
		var latest storedGrant
		if err != nil || len(raw) > 128<<10 || json.Unmarshal([]byte(raw), &latest) != nil || latest.Token.AccessToken == "" {
			return nil, errors.New("OAuth credentials are unavailable; authorize again")
		}
		if latest.Token.AccessToken != s.token.AccessToken || latest.Token.RefreshToken != s.token.RefreshToken || !latest.Token.Expiry.Equal(s.token.Expiry) || latest.Config.ClientID != s.config.ClientID {
			s.config, s.token = latest.Config, latest.Token
			s.base = s.config.TokenSource(s.ctx, &s.token)
		}
	}
	tok, err := s.base.Token()
	if err != nil {
		return nil, errors.New("OAuth token refresh failed; authorize again")
	}
	if s.observe != nil {
		s.observe(tok.AccessToken, tok.RefreshToken, s.config.ClientSecret)
	}
	if tok.AccessToken != s.token.AccessToken || tok.RefreshToken != s.token.RefreshToken || !tok.Expiry.Equal(s.token.Expiry) || tok.TokenType != s.token.TokenType {
		grant := storedGrant{Config: s.config, Token: *tok}
		raw, err := json.Marshal(grant)
		if err != nil || provider.SaveCredential(provider.CredentialFilePath(), s.name, string(raw)) != nil {
			return nil, errors.New("could not save refreshed OAuth credentials")
		}
		s.token = *tok
	}
	return tok, nil
}

func makeTokenSource(ctx context.Context, name string, cfg *oauth2.Config, token *oauth2.Token, observe func(...string)) (oauth2.TokenSource, error) {
	if cfg == nil || token == nil || token.AccessToken == "" {
		return nil, errors.New("invalid OAuth credentials")
	}
	path := provider.CredentialFilePath()
	if path == "" {
		return nil, errors.New("could not save OAuth credentials")
	}
	unlock, err := filelock.Acquire(path + ".oauth-refresh-" + name)
	if err != nil {
		return nil, errors.New("cannot lock OAuth credentials for saving")
	}
	defer unlock()
	raw, err := json.Marshal(storedGrant{Config: *cfg, Token: *token})
	if err != nil || provider.SaveCredential(path, name, string(raw)) != nil {
		return nil, errors.New("could not save OAuth credentials")
	}
	if observe != nil {
		observe(token.AccessToken, token.RefreshToken, cfg.ClientSecret)
	}
	return &tokenSource{name: name, config: *cfg, token: *token, base: cfg.TokenSource(ctx, token), ctx: ctx, observe: observe}, nil
}

// Stored returns a non-interactive handler for a previously authorized source.
// A 401/403 never launches a browser from an agent tool call.
func Stored(source, fingerprint, resourceURL string, observers ...func(...string)) (auth.OAuthHandler, error) {
	name := credentialName(source, fingerprint)
	path := provider.CredentialFilePath()
	credentials, err := provider.LoadCredentialFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("cannot read stored OAuth credentials")
	}
	raw, ok := credentials[name]
	if !ok || raw == "" {
		return nil, nil
	}
	var saved storedGrant
	if len(raw) > 128<<10 || json.Unmarshal([]byte(raw), &saved) != nil || saved.Token.AccessToken == "" {
		return nil, errors.New("stored OAuth credentials are invalid; authorize again")
	}
	client, err := safeClient(resourceURL)
	if err != nil {
		return nil, err
	}
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, client)
	var observe func(...string)
	if len(observers) > 0 {
		observe = observers[0]
		if observe != nil {
			observe(saved.Token.AccessToken, saved.Token.RefreshToken, saved.Config.ClientSecret)
		}
	}
	ts := &tokenSource{name: name, config: saved.Config, token: saved.Token, base: saved.Config.TokenSource(ctx, &saved.Token), ctx: ctx, observe: observe}
	return &nonInteractive{source: ts}, nil
}

type nonInteractive struct{ source oauth2.TokenSource }

func (h *nonInteractive) TokenSource(context.Context) (oauth2.TokenSource, error) {
	return h.source, nil
}

func (h *nonInteractive) Authorize(_ context.Context, _ *http.Request, response *http.Response) error {
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	return errors.New("authentication required; authorize this MCP server explicitly")
}

// Flow owns one loopback callback listener and one authorization transaction.
// It must be closed when the explicit operator action ends.
type Flow struct {
	handler  *auth.AuthorizationCodeHandler
	server   *http.Server
	listener net.Listener
	authMu   sync.Mutex
	mu       sync.Mutex
	state    string
	startURL string
	answer   chan callbackResult
	open     Open
	resource string
	closed   bool
}

type callbackResult struct {
	value *auth.AuthorizationResult
	err   error
}

// Registration optionally supplies a public pre-registered OAuth client ID.
// An HTTPS client ID with a non-root path can also be a metadata document.
type Registration struct {
	ClientID string
	Issuer   string
}

// New creates the listener before dynamic client registration so its exact
// redirect URI can be registered. It never starts a browser without Open.
func New(source, fingerprint, resourceURL string, open Open, observers ...func(...string)) (*Flow, error) {
	return NewWithClient(source, fingerprint, resourceURL, Registration{}, open, observers...)
}

// NewWithClient also accepts an explicitly configured public client. It does
// not support embedded confidential client secrets.
func NewWithClient(source, fingerprint, resourceURL string, registration Registration, open Open, observers ...func(...string)) (*Flow, error) {
	if open == nil {
		return nil, errors.New("operator authorization browser is unavailable")
	}
	client, err := safeClient(resourceURL)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, errors.New("cannot create a local OAuth callback listener")
	}
	path := "/mcp-callback/" + rand.Text()
	redirect := "http://" + listener.Addr().String() + path
	f := &Flow{listener: listener, answer: make(chan callbackResult, 1), open: open, resource: resourceURL}
	mux := http.NewServeMux()
	mux.HandleFunc(path, f.callback)
	startPath := "/mcp-start/" + rand.Text()
	mux.HandleFunc(startPath, f.start)
	f.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 16 << 10}
	name := credentialName(source, fingerprint)
	var observe func(...string)
	if len(observers) > 0 {
		observe = observers[0]
	}
	config := &auth.AuthorizationCodeHandlerConfig{
		RedirectURL: redirect,
		DynamicClientRegistrationConfig: &auth.DynamicClientRegistrationConfig{Metadata: &oauthex.ClientRegistrationMetadata{
			RedirectURIs: []string{redirect}, ClientName: "Golder", TokenEndpointAuthMethod: "none",
			GrantTypes: []string{"authorization_code", "refresh_token"}, ResponseTypes: []string{"code"},
		}},
		RequestRefreshToken: true,
		Client:              client,
		NewTokenSource: func(ctx context.Context, cfg *oauth2.Config, token *oauth2.Token) (oauth2.TokenSource, error) {
			return makeTokenSource(ctx, name, cfg, token, observe)
		},
		AuthorizationCodeFetcher: func(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
			return f.fetch(ctx, args, "http://"+listener.Addr().String()+startPath)
		},
	}
	if registration.ClientID != "" {
		config.PreregisteredClient = &oauthex.ClientCredentials{ClientID: registration.ClientID, Issuer: registration.Issuer}
		if u, parseErr := url.Parse(registration.ClientID); parseErr == nil && registration.Issuer == "" && u.Scheme == "https" && u.Host != "" && u.User == nil && u.Path != "" && u.Path != "/" && u.RawQuery == "" && u.Fragment == "" {
			config.ClientIDMetadataDocumentConfig = &auth.ClientIDMetadataDocumentConfig{URL: registration.ClientID}
		}
	}
	f.handler, err = auth.NewAuthorizationCodeHandler(config)
	if err != nil {
		_ = listener.Close()
		return nil, errors.New("cannot initialize OAuth client")
	}
	go func() { _ = f.server.Serve(listener) }()
	return f, nil
}

func (f *Flow) TokenSource(ctx context.Context) (oauth2.TokenSource, error) {
	return f.handler.TokenSource(ctx)
}

func (f *Flow) Authorize(ctx context.Context, request *http.Request, response *http.Response) error {
	f.authMu.Lock()
	defer f.authMu.Unlock()
	f.mu.Lock()
	closed := f.closed
	f.mu.Unlock()
	if closed {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return errors.New("authentication required; authorize this MCP server explicitly")
	}
	if existing, _ := f.handler.TokenSource(ctx); existing != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return errors.New("authentication requires a new explicit authorization")
	}
	if err := f.handler.Authorize(ctx, request, response); err != nil {
		// SDK errors can contain authorization URLs or remote response bodies.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, ErrBrowserUnavailable) || errors.Is(err, ErrAuthorizationDenied) {
			return err
		}
		return errors.New("OAuth authorization failed or was cancelled; check the provider and try again")
	}
	return nil
}

func (f *Flow) Close() {
	f.mu.Lock()
	f.closed, f.state, f.startURL = true, "", ""
	f.open = nil
	f.mu.Unlock()
	_ = f.server.Close()
	_ = f.listener.Close()
}

func (f *Flow) fetch(ctx context.Context, args *auth.AuthorizationArgs, startURL string) (*auth.AuthorizationResult, error) {
	if args == nil {
		return nil, errors.New("missing authorization request")
	}
	u, err := url.Parse(args.URL)
	if err != nil || u.User != nil || u.Hostname() == "" || u.Fragment != "" || !authorizationSchemeAllowed(u, f.resource) {
		return nil, errors.New("unsafe authorization endpoint")
	}
	states := u.Query()["state"]
	if len(states) != 1 || states[0] == "" || len(states[0]) > 1024 {
		return nil, errors.New("invalid authorization state")
	}
	host := u.Hostname()
	if net.ParseIP(host) == nil {
		host, err = idna.Lookup.ToASCII(host)
	}
	if err != nil || host == "" || len(host) > 253 {
		return nil, errors.New("invalid authorization domain")
	}
	if port := u.Port(); port != "" {
		host = net.JoinHostPort(host, port)
	}
	scopeText := u.Query().Get("scope")
	if len(scopeText) > 512 || strings.IndexFunc(scopeText, unicode.IsControl) >= 0 {
		return nil, errors.New("authorization scope is too large or invalid")
	}
	scopes := strings.Fields(scopeText)
	if len(scopes) > 32 {
		return nil, errors.New("too many authorization scopes")
	}
	f.mu.Lock()
	if f.closed || f.open == nil {
		f.mu.Unlock()
		return nil, errors.New("authorization transaction is closed")
	}
	f.state, f.startURL = states[0], args.URL
	open := f.open
	f.mu.Unlock()
	closeBrowser, err := open(ctx, Review{Host: host, Scopes: scopes}, startURL)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrBrowserUnavailable
	}
	if closeBrowser != nil {
		defer closeBrowser()
	}
	select {
	case result := <-f.answer:
		return result.value, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (f *Flow) start(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	target := f.startURL
	f.mu.Unlock()
	if r.Method != http.MethodGet || r.Host != f.listener.Addr().String() || target == "" {
		http.Error(w, "Authorization unavailable", http.StatusBadRequest)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, target, http.StatusFound)
}

func (f *Flow) callback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	q := r.URL.Query()
	f.mu.Lock()
	if r.Method != http.MethodGet || r.Host != f.listener.Addr().String() || f.closed || f.state == "" || len(q["state"]) != 1 || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(f.state)) != 1 || len(q["iss"]) > 1 || len(q["error"]) > 1 || len(q["code"]) > 1 || (q.Get("error") == "") == (q.Get("code") == "") || len(q.Get("code")) > 4096 {
		f.mu.Unlock()
		http.Error(w, "Authorization was not accepted. Return to Golder.", http.StatusBadRequest)
		return
	}
	f.state, f.startURL = "", "" // a callback is accepted exactly once
	f.mu.Unlock()
	answer := callbackResult{value: &auth.AuthorizationResult{Code: q.Get("code"), State: q.Get("state"), Iss: q.Get("iss")}}
	if q.Get("error") != "" {
		answer.value = nil
		answer.err = ErrAuthorizationDenied
	}
	select {
	case f.answer <- answer:
		_, _ = fmt.Fprint(w, "Authorization received. Return to Golder.")
	default:
		http.Error(w, "Authorization already received.", http.StatusConflict)
	}
}
