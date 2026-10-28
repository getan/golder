// Package extensions owns the host-side extension boundary. Discovery does not
// execute code; approved sources are connected only explicitly or after a tool gate.
package extensions

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/getan/golder/extension"
	"github.com/getan/golder/internal/golderhome"
)

type SourceRef = extension.SourceRef

// SourceConfig is an operator declaration, never a model argument. Network is
// the immutable launch ceiling of a local process, not a per-call grant.
type SourceConfig struct {
	Enabled         bool              `toml:"enabled" json:"enabled"`
	Runtime         string            `toml:"runtime,omitempty" json:"runtime,omitempty"`
	Manifest        string            `toml:"manifest,omitempty" json:"manifest,omitempty"`
	Transport       string            `toml:"transport,omitempty" json:"transport,omitempty"`
	Command         string            `toml:"command,omitempty" json:"command,omitempty"`
	Args            []string          `toml:"args,omitempty" json:"args,omitempty"`
	URL             string            `toml:"url,omitempty" json:"url,omitempty"`
	Dir             string            `toml:"dir,omitempty" json:"dir,omitempty"`
	Env             map[string]string `toml:"env,omitempty" json:"env,omitempty"`
	EnvPassthrough  []string          `toml:"env_passthrough,omitempty" json:"env_passthrough,omitempty"`
	Headers         map[string]string `toml:"headers,omitempty" json:"headers,omitempty"`
	AllowedTools    []string          `toml:"allowed_tools,omitempty" json:"allowed_tools,omitempty"`
	DisallowedTools []string          `toml:"disallowed_tools,omitempty" json:"disallowed_tools,omitempty"`
	StartupTimeout  string            `toml:"startup_timeout,omitempty" json:"startup_timeout,omitempty"`
	CallTimeout     string            `toml:"call_timeout,omitempty" json:"call_timeout,omitempty"`
	Network         bool              `toml:"network,omitempty" json:"network,omitempty"`
}

type Config struct {
	Plugins map[string]SourceConfig `toml:"plugins"`
	MCP     struct {
		Servers map[string]SourceConfig `toml:"servers"`
	} `toml:"mcp"`
}

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
var configWriteMu sync.Mutex

func ConfigPath() string { return golderhome.File(golderhome.ConfigFile) }

func LoadConfig(path string) (Config, error) {
	var cfg Config
	if path == "" {
		return cfg, nil
	}
	b, err := readLimited(path, 2<<20)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := toml.Unmarshal(b, &cfg); err != nil {
		return cfg, errors.New("invalid extension configuration TOML (contents withheld)")
	}
	// Semantic errors belong to individual sources. A malformed source must not
	// prevent the remaining extensions or the agent itself from being inspected.
	return cfg, nil
}

func validateConfig(ref SourceRef, c SourceConfig) error {
	if !idPattern.MatchString(ref.ID) || (ref.Kind != "plugin" && ref.Kind != "mcp") {
		return errors.New("invalid extension kind or id")
	}
	for _, raw := range []string{c.StartupTimeout, c.CallTimeout} {
		if raw != "" {
			d, err := time.ParseDuration(raw)
			if err != nil || d <= 0 || d > 10*time.Minute {
				return fmt.Errorf("%s: timeout must be positive and at most 10m", ref)
			}
		}
	}
	for _, p := range append(append([]string{}, c.AllowedTools...), c.DisallowedTools...) {
		if _, err := filepath.Match(p, "tool"); err != nil {
			return fmt.Errorf("%s: invalid tool filter", ref)
		}
	}
	if ref.Kind == "plugin" {
		if c.Runtime == "cli" && c.Manifest != "" {
			return nil
		}
		if c.Runtime == "rpc" && c.Command != "" {
			return nil
		}
		return fmt.Errorf("%s: expected runtime=cli with manifest, or runtime=rpc with command", ref)
	}
	if c.Transport == "stdio" && c.Command != "" {
		return nil
	}
	if c.Transport != "http" {
		return fmt.Errorf("%s: expected stdio with command, or http with url", ref)
	}
	u, err := url.Parse(c.URL)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("%s: invalid endpoint", ref)
	}
	loop := u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"
	if u.Scheme != "https" && !(u.Scheme == "http" && loop) {
		return fmt.Errorf("%s: HTTPS required except loopback HTTP", ref)
	}
	return nil
}

func readLimited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(b)) > limit {
		return nil, errors.New("extension file exceeds size limit")
	}
	return b, err
}

func expandPath(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, e := os.UserHomeDir(); e == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

func timeout(raw string, fallback time.Duration) time.Duration {
	if d, e := time.ParseDuration(raw); e == nil && d > 0 {
		return d
	}
	return fallback
}

// WriteSource edits just one canonical source table, preserving unrelated
// tables/comments. Noncanonical dotted/inline representations fail closed.
func WriteSource(path string, ref SourceRef, c *SourceConfig) error {
	configWriteMu.Lock()
	defer configWriteMu.Unlock()
	if c != nil {
		if err := validateConfig(ref, *c); err != nil {
			return err
		}
	}
	if !idPattern.MatchString(ref.ID) {
		return errors.New("invalid extension id")
	}
	b, err := readLimited(path, 2<<20)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	prefix := "plugins." + ref.ID
	if ref.Kind == "mcp" {
		prefix = "mcp.servers." + ref.ID
	} else if ref.Kind != "plugin" {
		return errors.New("invalid kind")
	}
	var kept []string
	skip := false
	for _, line := range strings.Split(string(b), "\n") {
		s := strings.TrimSpace(line)
		if strings.HasPrefix(s, "[") {
			end := strings.Index(s, "]")
			if end < 0 {
				return errors.New("cannot edit noncanonical extension config")
			}
			table := strings.TrimSpace(s[1:end])
			skip = table == prefix || strings.HasPrefix(table, prefix+".")
		}
		if !skip {
			kept = append(kept, line)
		}
	}
	var out bytes.Buffer
	out.WriteString(strings.TrimRight(strings.Join(kept, "\n"), "\n"))
	out.WriteString("\n\n")
	if c != nil {
		var doc any
		if ref.Kind == "mcp" {
			doc = map[string]any{"mcp": map[string]any{"servers": map[string]any{ref.ID: *c}}}
		} else {
			doc = map[string]any{"plugins": map[string]any{ref.ID: *c}}
		}
		var encoded bytes.Buffer
		if err := toml.NewEncoder(&encoded).Encode(doc); err != nil {
			return err
		}
		for _, line := range strings.Split(encoded.String(), "\n") {
			// The encoder emits parent tables too. A source table already
			// implies these parents; repeating them would break a second add.
			s := strings.TrimSpace(line)
			if s == "[plugins]" || s == "[mcp]" || s == "[mcp.servers]" {
				continue
			}
			out.WriteString(line + "\n")
		}
	}
	var before, after map[string]any
	if toml.Unmarshal(b, &before) != nil || toml.Unmarshal(out.Bytes(), &after) != nil {
		return errors.New("configuration cannot be safely edited; edit the source table by hand")
	}
	if c != nil {
		var checked Config
		if toml.Unmarshal(out.Bytes(), &checked) != nil {
			return errors.New("invalid resulting extension config")
		}
		actual := checked.Plugins[ref.ID]
		if ref.Kind == "mcp" {
			actual = checked.MCP.Servers[ref.ID]
		}
		want, _ := json.Marshal(c)
		got, _ := json.Marshal(actual)
		if !bytes.Equal(want, got) {
			return errors.New("source table did not round-trip safely")
		}
	}
	// Check all unrelated data survived. This also catches duplicate/inline tables.
	drop := func(doc map[string]any) {
		if ref.Kind == "plugin" {
			if m, ok := doc["plugins"].(map[string]any); ok {
				delete(m, ref.ID)
				if len(m) == 0 {
					delete(doc, "plugins")
				}
			}
		} else if m, ok := doc["mcp"].(map[string]any); ok {
			if ss, ok := m["servers"].(map[string]any); ok {
				delete(ss, ref.ID)
				if len(ss) == 0 {
					delete(m, "servers")
				}
			}
			if len(m) == 0 {
				delete(doc, "mcp")
			}
		}
	}
	drop(before)
	drop(after)
	a, _ := json.Marshal(before)
	z, _ := json.Marshal(after)
	if !bytes.Equal(a, z) {
		return errors.New("configuration edit would change unrelated settings")
	}
	return atomicWrite(path, out.Bytes())
}

func atomicWrite(path string, data []byte) error {
	if path == "" {
		return errors.New("golder state root unavailable")
	}
	if info, e := os.Lstat(path); e == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return errors.New("refusing non-regular state file")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".extension-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0o600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err == nil {
		err = ce
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

// fingerprint includes actual executable bytes and manifest content. PATH
// retargeting or replacing a trusted executable invalidates approval.
func fingerprint(ref SourceRef, c SourceConfig, cwd string) (string, error) {
	c.Enabled = false
	h := sha256.New()
	b, _ := json.Marshal(struct {
		Ref    SourceRef
		Config SourceConfig
		Cwd    string
	}{ref, c, cwd})
	h.Write(b)
	if c.Manifest != "" {
		p := expandPath(c.Manifest)
		if !filepath.IsAbs(p) {
			return "", errors.New("manifest path must be absolute")
		}
		b, e := readLimited(p, 1<<20)
		if e != nil {
			return "", e
		}
		h.Write(b)
		mf, e := loadManifest(p)
		if e != nil {
			return "", e
		}
		for _, t := range mf.Tools {
			if e = hashProgram(h, t.Command[0]); e != nil {
				return "", e
			}
		}
	} else if c.Command != "" {
		if e := hashProgram(h, c.Command); e != nil {
			return "", e
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func hashProgram(h io.Writer, program string) error {
	p, e := resolvedProgram(program)
	if e != nil {
		return e
	}
	io.WriteString(h, p)
	f, e := os.Open(p)
	if e != nil {
		return e
	}
	defer f.Close()
	_, e = io.Copy(h, f)
	return e
}

func resolvedProgram(program string) (string, error) {
	p, e := exec.LookPath(expandPath(program))
	if e != nil {
		return "", errors.New("extension executable not found")
	}
	p, e = filepath.Abs(p)
	if e != nil {
		return "", e
	}
	p, e = filepath.EvalSymlinks(p)
	if e != nil {
		return "", e
	}
	info, e := os.Stat(p)
	if e != nil {
		return "", e
	}
	if !info.Mode().IsRegular() || info.Size() > 256<<20 {
		return "", errors.New("extension executable must be a regular file no larger than 256 MiB")
	}
	return p, nil
}
