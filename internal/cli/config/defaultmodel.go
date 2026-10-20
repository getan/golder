package config

// The default model and reasoning level are two top-level keys in config.toml,
// edited the same way as the default provider: find the line and replace it, so
// comments, tables and hand-tuned settings survive. A model and the gateway that
// serves it are written together, because an id alone can resolve to a different
// provider on the next start.

import (
	"fmt"
	"os"
	"strings"
)

const thinkingKey = "thinking_level"

// DefaultModel returns the top-level `model` value, or "" when the file does not
// set one.
func DefaultModel() (string, error) { return topLevelValue(modelKey) }

// DefaultThinkingLevel returns the top-level `thinking_level` value, or "".
func DefaultThinkingLevel() (string, error) { return topLevelValue(thinkingKey) }

func topLevelValue(key string) (string, error) {
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
	_, value, _ := findTopLevelKey(string(data), key)
	return value, nil
}

// SetDefaultModel writes the default model, reasoning level and (when non-empty)
// the provider that serves it. An empty model or level removes its key. The
// provider key is left alone when providerName is empty, so clearing a model
// does not also forget which gateway a new session should open with.
func SetDefaultModel(model, thinkingLevel, providerName string) error {
	path := FileConfigPath()
	if path == "" {
		return fmt.Errorf("cannot locate the config directory")
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read config %s: %w", path, err)
	}
	text := string(data)
	hadTrailingNewline := text == "" || strings.HasSuffix(text, "\n")
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if text == "" {
		lines = nil
	}

	lines = setTopLevel(lines, modelKey, strings.TrimSpace(model))
	lines = setTopLevel(lines, thinkingKey, strings.TrimSpace(thinkingLevel))
	if provider := strings.TrimSpace(providerName); provider != "" {
		lines = setTopLevel(lines, providerKey, provider)
	}

	out := strings.Join(lines, "\n")
	if hadTrailingNewline && out != "" {
		out += "\n"
	}
	if out == text {
		return nil
	}
	return writeConfigFile(path, out)
}

// SetDefaultThinkingLevel writes only the reasoning level, leaving the model and
// provider untouched. It backs the level picker's default key.
func SetDefaultThinkingLevel(thinkingLevel string) error {
	path := FileConfigPath()
	if path == "" {
		return fmt.Errorf("cannot locate the config directory")
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read config %s: %w", path, err)
	}
	text := string(data)
	hadTrailingNewline := text == "" || strings.HasSuffix(text, "\n")
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if text == "" {
		lines = nil
	}
	lines = setTopLevel(lines, thinkingKey, strings.TrimSpace(thinkingLevel))
	out := strings.Join(lines, "\n")
	if hadTrailingNewline && out != "" {
		out += "\n"
	}
	if out == text {
		return nil
	}
	return writeConfigFile(path, out)
}

// setTopLevel replaces or inserts one top-level key. An empty value removes the
// key; an absent key with an empty value is a no-op.
func setTopLevel(lines []string, key, value string) []string {
	at, _, found := findTopLevelKeyLine(lines, key)
	switch {
	case value == "" && found:
		return append(append([]string{}, lines[:at]...), lines[at+1:]...)
	case value == "":
		return lines
	case found:
		out := append([]string{}, lines...)
		out[at] = key + " = " + quoteTOMLString(value)
		return out
	default:
		return insertTopLevelLine(lines, key+" = "+quoteTOMLString(value))
	}
}
