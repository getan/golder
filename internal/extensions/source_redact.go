package extensions

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const maxSourceSecrets = 512

// AddSecrets tracks exact values sent to this source, independently of the
// optional global redactor. Never persist or return the values themselves.
func (b *logBuffer) AddSecrets(values ...string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, value := range values {
		if value == "" {
			continue
		}
		candidates := []string{value}
		if strings.HasPrefix(strings.ToLower(value), "bearer ") {
			candidates = append(candidates, strings.TrimSpace(value[len("bearer "):]))
		}
		for _, candidate := range candidates {
			if candidate == "" || slices.Contains(b.secrets, candidate) {
				continue
			}
			if len(candidate) > 256<<10 || len(b.secrets) >= maxSourceSecrets {
				b.withhold = true
				continue
			}
			b.secrets = append(b.secrets, candidate)
		}
	}
	slices.SortFunc(b.secrets, func(a, c string) int { return len(c) - len(a) })
}

func (b *logBuffer) secretSnapshot() ([]string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.secrets...), b.withhold || b.unresolved
}

func (b *logBuffer) Clean(text string, limit int) string {
	secrets, withhold := b.secretSnapshot()
	if withhold {
		return "[source output withheld: credentials unavailable or protection limit]"
	}
	for _, secret := range secrets {
		text = strings.ReplaceAll(text, secret, "[credential withheld]")
	}
	return clean(text, limit)
}

// ContainsSecretJSON checks decoded strings and keys, so JSON escaping cannot
// hide a credential from the source-local output filter.
func (b *logBuffer) ContainsSecretJSON(raw []byte) bool {
	secrets, withhold := b.secretSnapshot()
	if withhold {
		return true
	}
	if len(secrets) == 0 {
		return false
	}
	var root any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&root) != nil {
		return true
	}
	contains := func(text string) bool {
		for _, secret := range secrets {
			if strings.Contains(text, secret) {
				return true
			}
		}
		return false
	}
	var scan func(any, int) bool
	scan = func(value any, depth int) bool {
		if depth > 128 {
			return true
		}
		switch v := value.(type) {
		case string:
			return contains(v)
		case json.Number:
			return contains(string(v))
		case []any:
			for _, item := range v {
				if scan(item, depth+1) {
					return true
				}
			}
		case map[string]any:
			for key, item := range v {
				if contains(key) || scan(item, depth+1) {
					return true
				}
			}
		}
		return false
	}
	return scan(root, 0)
}

func (b *logBuffer) safeCatalog(catalog Catalog) bool {
	raw, err := json.Marshal(catalog)
	return err == nil && !b.ContainsSecretJSON(raw)
}

func secretLikeName(name string) bool {
	name = strings.ToUpper(name)
	for _, part := range []string{"TOKEN", "SECRET", "PASSWORD", "PASSWD", "API_KEY", "APIKEY", "CREDENTIAL", "AUTHORIZATION"} {
		if strings.Contains(name, part) {
			return true
		}
	}
	return false
}

// registerConfigSecrets resolves only explicitly configured values; discovery
// still never launches a process or contacts an MCP endpoint.
func (b *logBuffer) registerConfigSecrets(cfg SourceConfig) {
	unresolved := false
	for key, raw := range cfg.Env {
		if secretLikeName(key) || strings.Contains(raw, "${") {
			if value, err := resolveValue(raw); err == nil {
				b.AddSecrets(value)
			} else {
				unresolved = true
			}
		}
	}
	for _, raw := range cfg.Headers {
		if value, err := resolveValue(raw); err == nil {
			b.AddSecrets(value)
		} else {
			unresolved = true
		}
	}
	for _, pattern := range cfg.EnvPassthrough {
		for _, pair := range os.Environ() {
			name, value, ok := strings.Cut(pair, "=")
			if match, _ := filepath.Match(pattern, name); ok && match && !strings.HasPrefix(name, "GOLDER_") {
				b.AddSecrets(value)
			}
		}
	}
	b.mu.Lock()
	b.unresolved = unresolved
	b.mu.Unlock()
}
