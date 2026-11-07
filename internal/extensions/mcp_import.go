package extensions

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/getan/golder/internal/filelock"
	"github.com/getan/golder/internal/provider"
)

const maxMCPImportBytes = 1 << 20

var (
	mcpReference  = regexp.MustCompile(`^\$\{(?:credential:[a-zA-Z0-9_.-]+|[A-Za-z_][A-Za-z0-9_]*)\}$`)
	mcpEnvName    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	mcpHeaderName = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
)

// MCPDraft is a transient operator-only import. Env and Headers may contain
// secrets and must never be logged, displayed in a transcript or marshalled.
type MCPDraft struct {
	ID            string
	Transport     string
	Command       string
	Args          []string
	URL           string
	Network       bool
	OAuthClientID string
	OAuthIssuer   string
	Env           map[string]string `json:"-"`
	Headers       map[string]string `json:"-"`
}

type mcpImportServer struct {
	Type          string            `json:"type"`
	Transport     string            `json:"transport"`
	Command       string            `json:"command"`
	Args          []string          `json:"args"`
	URL           string            `json:"url"`
	Env           map[string]string `json:"env"`
	Headers       map[string]string `json:"headers"`
	OAuthClientID string            `json:"oauth_client_id"`
	OAuthIssuer   string            `json:"oauth_issuer"`
}

// ParseMCPImport accepts an mcpServers object or one server object. It performs
// no I/O and never includes untrusted configuration values in errors.
func ParseMCPImport(raw string) ([]MCPDraft, error) {
	if len(raw) == 0 || len(raw) > maxMCPImportBytes {
		return nil, errors.New("MCP configuration is empty or exceeds 1 MiB")
	}
	if err := rejectDuplicateJSON([]byte(raw)); err != nil {
		return nil, errors.New("invalid MCP JSON or duplicate field")
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &top); err != nil || len(top) == 0 {
		return nil, errors.New("expected an MCP JSON object")
	}
	servers := map[string]json.RawMessage{}
	if data, ok := top["mcpServers"]; ok {
		if len(top) != 1 || json.Unmarshal(data, &servers) != nil || len(servers) == 0 {
			return nil, errors.New("expected a nonempty mcpServers object without extra top-level fields")
		}
	} else {
		servers["mcp"] = json.RawMessage(raw)
	}
	if len(servers) > 64 {
		return nil, errors.New("at most 64 MCP servers can be imported at once")
	}
	ids := make([]string, 0, len(servers))
	for id := range servers {
		if !idPattern.MatchString(id) {
			return nil, errors.New("MCP names must use lowercase letters, digits, hyphens or underscores (up to 32 characters)")
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	drafts := make([]MCPDraft, 0, len(ids))
	for _, id := range ids {
		var server mcpImportServer
		dec := json.NewDecoder(bytes.NewReader(servers[id]))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&server); err != nil {
			return nil, fmt.Errorf("mcp:%s has an unsupported field or invalid value", id)
		}
		if server.Type != "" && server.Transport != "" && server.Type != server.Transport {
			return nil, fmt.Errorf("mcp:%s has conflicting transport fields", id)
		}
		kind := server.Type
		if kind == "" {
			kind = server.Transport
		}
		if kind == "" {
			if server.URL != "" {
				kind = "http"
			} else {
				kind = "stdio"
			}
		}
		if kind == "streamable-http" {
			kind = "http"
		}
		if kind != "stdio" && kind != "http" {
			return nil, fmt.Errorf("mcp:%s: only stdio and streamable HTTP are supported", id)
		}
		if kind == "http" && (server.Command != "" || len(server.Args) != 0 || len(server.Env) != 0) ||
			kind == "stdio" && (server.URL != "" || len(server.Headers) != 0) {
			return nil, fmt.Errorf("mcp:%s mixes HTTP and local command fields", id)
		}
		// Query strings can contain credentials and cannot safely be displayed,
		// approved and persisted as ordinary configuration. Use headers instead.
		if kind == "http" {
			u, err := url.Parse(server.URL)
			if err != nil || u.RawQuery != "" || u.ForceQuery {
				return nil, fmt.Errorf("mcp:%s: endpoint query strings are not supported; use headers for credentials", id)
			}
		}
		draft := MCPDraft{ID: id, Transport: kind, Command: server.Command, Args: server.Args, URL: server.URL, Env: server.Env, Headers: server.Headers, OAuthClientID: server.OAuthClientID, OAuthIssuer: server.OAuthIssuer}
		if err := draft.Validate(); err != nil {
			return nil, err
		}
		drafts = append(drafts, draft)
	}
	return drafts, nil
}

// Validate is data-only. Local executable availability is checked when the
// manager fingerprints the source, never by executing it.
func (d MCPDraft) Validate() error {
	ref := SourceRef{Kind: "mcp", ID: d.ID}
	cfg := SourceConfig{Transport: d.Transport, Command: d.Command, Args: d.Args, URL: d.URL, Network: d.Network, OAuthClientID: d.OAuthClientID, OAuthIssuer: d.OAuthIssuer}
	if err := validateConfig(ref, cfg); err != nil {
		return fmt.Errorf("invalid MCP source: %w", err)
	}
	if d.Transport == "http" {
		if d.Command != "" || len(d.Args) != 0 || len(d.Env) != 0 {
			return errors.New("HTTP source cannot contain local command fields")
		}
		if d.OAuthClientID != "" && hasAuthorizationHeader(d.Headers) {
			return errors.New("choose either an Authorization header or OAuth client settings, not both")
		}
		u, err := url.Parse(d.URL)
		if err != nil || u.RawQuery != "" || u.ForceQuery {
			return errors.New("endpoint query strings are not supported; use headers for credentials")
		}
	} else if d.URL != "" || len(d.Headers) != 0 {
		return errors.New("local source cannot contain HTTP fields")
	}
	if len(d.Args) > 256 || strings.ContainsRune(d.Command, 0) {
		return errors.New("invalid MCP command or too many arguments")
	}
	if containsSecretArgument(d.Args) {
		return errors.New("secret-like command arguments are stored as plain text; use environment variables instead")
	}
	commandBytes := len(d.Command) + len(d.URL)
	for _, arg := range d.Args {
		if len(arg) > 64<<10 || strings.ContainsRune(arg, 0) {
			return errors.New("invalid MCP command argument")
		}
		commandBytes += len(arg)
	}
	if commandBytes > 64<<10 {
		return errors.New("MCP endpoint or command exceeds the review size limit")
	}
	for i, values := range []map[string]string{d.Env, d.Headers} {
		if len(values) > 128 {
			return errors.New("too many MCP environment variables or headers")
		}
		for key, value := range values {
			validName := i == 0 && mcpEnvName.MatchString(key) || i == 1 && mcpHeaderName.MatchString(key)
			if !validName || len(key) > 256 || len(value) == 0 || len(value) > 64<<10 || strings.ContainsAny(value, "\x00\r\n") || value != strings.TrimSpace(value) {
				return errors.New("invalid MCP environment variable or header")
			}
			if i == 0 && strings.HasPrefix(strings.ToUpper(key), "GOLDER_") {
				return errors.New("GOLDER_* environment variables are reserved")
			}
			if strings.Contains(value, "${") && !mcpReference.MatchString(value) && !(strings.HasPrefix(value, "Bearer ") && mcpReference.MatchString(strings.TrimPrefix(value, "Bearer "))) {
				return errors.New("mixed environment or credential interpolation is unsupported; use a single reference or a literal value")
			}
		}
	}
	return nil
}

// Arguments must remain fixed literal argv for execution and cannot reference
// the named credential resolver. Refuse common secret-bearing flags rather
// than silently writing obvious credentials in plaintext configuration.
func containsSecretArgument(args []string) bool {
	for _, arg := range args {
		name, _, hasValue := strings.Cut(arg, "=")
		if !strings.HasPrefix(name, "-") && !hasValue {
			continue
		}
		name = strings.ToLower(strings.TrimLeft(name, "-"))
		name = strings.NewReplacer("-", "", "_", "").Replace(name)
		switch name {
		case "token", "accesstoken", "refreshtoken", "secret", "clientsecret", "password", "passwd", "apikey", "authorization":
			return true
		}
	}
	return false
}

func protectMCPValues(id string, values map[string]string) (map[string]string, []string, error) {
	if len(values) == 0 {
		return nil, nil, nil
	}
	out := make(map[string]string, len(values))
	var created []string
	for key, value := range values {
		if mcpReference.MatchString(value) || strings.HasPrefix(value, "Bearer ") && mcpReference.MatchString(strings.TrimPrefix(value, "Bearer ")) {
			out[key] = value
			continue
		}
		name := "mcp-" + id + "-" + strings.ToLower(rand.Text()[:16])
		if err := provider.CreateCredential(provider.CredentialFilePath(), name, value); err != nil {
			return nil, created, errors.New("could not save MCP credential")
		}
		created = append(created, name)
		out[key] = "${credential:" + name + "}"
	}
	return out, created, nil
}

func removeCreatedMCPCredentials(names []string) {
	for _, name := range names {
		_ = provider.DeleteCredential(provider.CredentialFilePath(), name)
	}
}

// AddMCP persists one disabled source and its private values. It rejects an
// existing ID while holding the cross-process config lock. Each inline value
// gets a new, unshared credential so rollback cannot delete another source's.
// No program is started and no remote server is contacted.
func (m *Manager) AddMCP(d MCPDraft) (err error) {
	if m == nil || m.closed.Load() {
		return errors.New("extension manager unavailable")
	}
	if err := d.Validate(); err != nil {
		return err
	}
	ref := SourceRef{Kind: "mcp", ID: d.ID}
	cfg := SourceConfig{Transport: d.Transport, Command: d.Command, Args: append([]string(nil), d.Args...), URL: d.URL, Network: d.Network, OAuthClientID: d.OAuthClientID, OAuthIssuer: d.OAuthIssuer}
	var created []string
	committed := false
	defer func() {
		if !committed {
			removeCreatedMCPCredentials(created)
		}
	}()
	var names []string
	cfg.Env, names, err = protectMCPValues(d.ID, d.Env)
	created = append(created, names...)
	if err != nil {
		return err
	}
	cfg.Headers, names, err = protectMCPValues(d.ID, d.Headers)
	created = append(created, names...)
	if err != nil {
		return err
	}
	if _, err = fingerprint(ref, cfg, m.opts.Cwd); err != nil {
		return fmt.Errorf("cannot review MCP executable: %w", err)
	}
	configWriteMu.Lock()
	defer configWriteMu.Unlock()
	unlock, err := filelock.Acquire(m.opts.ConfigPath)
	if err != nil {
		return err
	}
	defer unlock()
	current, err := LoadConfig(m.opts.ConfigPath)
	if err != nil {
		return err
	}
	if _, exists := current.MCP.Servers[d.ID]; exists || m.HasSource(ref) {
		return errors.New("MCP source already exists; reload or choose another name")
	}
	if err = writeSourceLocked(m.opts.ConfigPath, ref, &cfg); err != nil {
		return err
	}
	committed = true
	if err = m.ConfigureSource(ref, cfg); err != nil {
		return fmt.Errorf("MCP saved disabled; reload extensions: %w", err)
	}
	return nil
}

// rejectDuplicateJSON prevents ambiguous duplicate keys at every object depth.
func rejectDuplicateJSON(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	var value func(int) error
	value = func(depth int) error {
		if depth > 64 {
			return errors.New("JSON nesting limit exceeded")
		}
		token, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				s, ok := key.(string)
				if !ok || seen[s] {
					return errors.New("duplicate key")
				}
				seen[s] = true
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		default:
			return errors.New("unexpected delimiter")
		}
		_, err = d.Token()
		return err
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}
