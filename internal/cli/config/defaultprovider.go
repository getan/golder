package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The default provider is a line in config.toml, and this file edits that line.
//
// It edits rather than re-encodes, and that is the whole design: config.toml is
// a file people keep comments and hand-tuned settings in, so decoding it into a
// struct and writing it back would silently drop both. The same reason
// proxy.toml exists separately — a managed file must not rewrite a hand-written
// one — applies here to the key inside it.

// DefaultProvider returns the top-level `provider` value config.toml sets, or ""
// when the file does not set one.
func DefaultProvider() (string, error) {
	path := FileConfigPath()
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read config %s: %w", path, err)
	}
	_, value, found := findTopLevelKey(string(data), providerKey)
	if !found {
		return "", nil
	}
	return value, nil
}

// SetDefaultProvider writes the top-level provider key, and — when model is
// non-empty and the file already pins a model — rewrites that line too, because
// a model id belongs to the gateway that serves it. An empty name removes the
// key. It reports whether the model line changed.
//
// Comments, table bodies and every other key survive untouched: only the two
// lines are replaced, and a missing key is appended to the top-level block.
func SetDefaultProvider(name, model string) (bool, error) {
	path := FileConfigPath()
	if path == "" {
		return false, fmt.Errorf("cannot locate the config directory")
	}
	name = strings.TrimSpace(name)
	model = strings.TrimSpace(model)

	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("read config %s: %w", path, err)
	}
	text := string(data)
	hadTrailingNewline := text == "" || strings.HasSuffix(text, "\n")
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if text == "" {
		lines = nil
	}

	providerAt, _, hasProvider := findTopLevelKeyLine(lines, providerKey)
	_, modelValue, hasModel := findTopLevelKeyLine(lines, modelKey)

	switch {
	case name == "" && hasProvider:
		lines = append(lines[:providerAt], lines[providerAt+1:]...)
	case name == "":
		// Nothing to clear.
	case hasProvider:
		lines[providerAt] = providerKey + " = " + quoteTOMLString(name)
	default:
		lines = insertTopLevelLine(lines, providerKey+" = "+quoteTOMLString(name))
	}

	rewroteModel := false
	if model != "" && hasModel && modelValue != model {
		// The model index shifts by at most one line when the provider line was
		// just removed; re-find it rather than trusting the old offset.
		at, _, ok := findTopLevelKeyLine(lines, modelKey)
		if ok {
			lines[at] = modelKey + " = " + quoteTOMLString(model)
			rewroteModel = true
		}
	}

	out := strings.Join(lines, "\n")
	if hadTrailingNewline && out != "" {
		out += "\n"
	}
	if out == text {
		return rewroteModel, nil
	}
	return rewroteModel, writeConfigFile(path, out)
}

// writeConfigFile replaces config.toml atomically, keeping it 0600.
func writeConfigFile(path, out string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("cannot create the config directory")
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.toml")
	if err != nil {
		return fmt.Errorf("cannot create the config file")
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(out); err != nil {
		tmp.Close()
		return fmt.Errorf("cannot write the config file")
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("cannot close the config file")
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return fmt.Errorf("cannot protect the config file")
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("cannot save the config file")
	}
	return nil
}

const (
	providerKey = "provider"
	modelKey    = "model"
)

// findTopLevelKey reads a key from the text before the first table header. A
// `provider` inside `[dream]` is a different setting, so the scan stops at the
// first line that opens a table.
func findTopLevelKey(text, key string) (line string, value string, found bool) {
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	at, value, found := findTopLevelKeyLine(lines, key)
	if !found {
		return "", "", false
	}
	return lines[at], value, true
}

// findTopLevelKeyLine is findTopLevelKey over already-split lines, returning the
// index so the caller can replace it.
func findTopLevelKeyLine(lines []string, key string) (int, string, bool) {
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			return 0, "", false
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		name, value, ok := strings.Cut(trimmed, "=")
		if !ok || strings.TrimSpace(name) != key {
			continue
		}
		return i, unquoteTOMLString(value), true
	}
	return 0, "", false
}

// insertTopLevelLine adds the line after the last top-level entry, so a table
// below it stays below it.
func insertTopLevelLine(lines []string, line string) []string {
	at := 0
	for i, existing := range lines {
		if strings.HasPrefix(strings.TrimSpace(existing), "[") {
			break
		}
		at = i + 1
	}
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:at]...)
	out = append(out, line)
	out = append(out, lines[at:]...)
	return out
}

func quoteTOMLString(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}

func unquoteTOMLString(raw string) string {
	s := strings.TrimSpace(raw)
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	if at := strings.Index(s, "#"); at >= 0 {
		s = strings.TrimSpace(s[:at])
	}
	return s
}
