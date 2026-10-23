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
const modelDefaultsTable = "model_defaults"
const thinkingDefaultsTable = "thinking_defaults"

// DefaultModel returns the top-level `model` value, or "" when the file does not
// set one.
func DefaultModel() (string, error) { return topLevelValue(modelKey) }

// DefaultThinkingLevel returns the top-level `thinking_level` value, or "".
func DefaultThinkingLevel() (string, error) { return topLevelValue(thinkingKey) }

// DefaultThinkingLevelFor returns the reasoning level saved for one
// provider/model pair, or "" when none is saved. Unlike DefaultThinkingLevel it
// never falls back to the shared top-level value: a level set for one model
// must not become another model's default.
func DefaultThinkingLevelFor(providerName, model string) (string, error) {
	providerName = strings.TrimSpace(providerName)
	model = strings.TrimSpace(model)
	if providerName == "" || model == "" {
		return "", nil
	}
	return tableValue(thinkingDefaultsTable, thinkingDefaultKey(providerName, model))
}

// DefaultModelFor returns the default model saved for one provider. A config
// written before the per-provider table existed still works: the top-level
// model is used when it was saved for the same provider.
func DefaultModelFor(providerName string) (string, error) {
	providerName = strings.TrimSpace(providerName)
	if providerName == "" {
		return "", nil
	}
	if model, err := tableValue(modelDefaultsTable, providerName); err != nil || model != "" {
		return model, err
	}
	owner, err := DefaultProvider()
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(owner, providerName) {
		return "", nil
	}
	return DefaultModel()
}

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

// SetDefaultThinkingLevelFor writes (or removes, when level is empty) the
// reasoning level for one provider/model pair. The model, the provider and
// every other pair's level stay untouched.
func SetDefaultThinkingLevelFor(providerName, model, level string) error {
	path := FileConfigPath()
	if path == "" {
		return fmt.Errorf("cannot locate the config directory")
	}
	providerName = strings.TrimSpace(providerName)
	model = strings.TrimSpace(model)
	if providerName == "" || model == "" {
		return fmt.Errorf("cannot save a default reasoning level without a provider and model")
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

	lines = editThinkingDefault(lines, providerName, model, strings.TrimSpace(level))

	out := strings.Join(lines, "\n")
	if hadTrailingNewline && out != "" {
		out += "\n"
	}
	if out == text {
		return nil
	}
	return writeConfigFile(path, out)
}

// SetDefaultModelFor writes the per-provider default model and, for a non-empty
// model, the top-level provider/model pair so a new session starts there. An
// empty model removes only this provider's entry (and the top-level model when
// it pointed at this provider). A non-empty level is saved for this exact
// provider/model pair, never as a shared default.
func SetDefaultModelFor(providerName, model, thinkingLevel string) error {
	path := FileConfigPath()
	if path == "" {
		return fmt.Errorf("cannot locate the config directory")
	}
	providerName = strings.TrimSpace(providerName)
	model = strings.TrimSpace(model)
	if providerName == "" {
		return fmt.Errorf("cannot save a default model without a provider")
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

	lines = setTableValue(lines, modelDefaultsTable, providerName, model)
	if model != "" {
		lines = setTopLevel(lines, modelKey, model)
		lines = setTopLevel(lines, providerKey, providerName)
	} else if owner, _ := DefaultProvider(); strings.EqualFold(owner, providerName) {
		lines = setTopLevel(lines, modelKey, "")
	}
	if thinkingLevel = strings.TrimSpace(thinkingLevel); thinkingLevel != "" && model != "" {
		lines = editThinkingDefault(lines, providerName, model, thinkingLevel)
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

// editThinkingDefault writes or removes one [thinking_defaults] row. Before the
// first row exists, the legacy top-level thinking_level is migrated to
// per-pair rows for every saved default model, then removed.
func editThinkingDefault(lines []string, providerName, model, level string) []string {
	if strings.TrimSpace(level) != "" {
		lines = migrateLegacyThinkingLevel(lines)
	}
	return setTableValue(lines, thinkingDefaultsTable, thinkingDefaultKey(providerName, model), level)
}

// migrateLegacyThinkingLevel converts the old global thinking_level into
// per-model rows. It runs once: the presence of [thinking_defaults] means the
// config has already moved to scoped levels.
func migrateLegacyThinkingLevel(lines []string) []string {
	if _, _, ok := findTOMLTable(lines, thinkingDefaultsTable); ok {
		return lines
	}
	_, global, found := findTopLevelKeyLine(lines, thinkingKey)
	if !found || strings.TrimSpace(global) == "" {
		return lines
	}
	for _, entry := range tableEntries(lines, modelDefaultsTable) {
		if entry.Key == "" || entry.Value == "" {
			continue
		}
		lines = setTableValue(lines, thinkingDefaultsTable, thinkingDefaultKey(entry.Key, entry.Value), global)
	}
	return setTopLevel(lines, thinkingKey, "")
}

// thinkingDefaultKey names one row in [thinking_defaults]. Provider names come
// from the built-in registry and never contain "/", while model ids may, so the
// first slash is an unambiguous separator.
func thinkingDefaultKey(providerName, model string) string {
	return providerName + "/" + model
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

// tableValue reads one key from a TOML table. A missing file or table reads as
// empty, like the top-level readers.
func tableValue(table, key string) (string, error) {
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
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	start, end, ok := findTOMLTable(lines, table)
	if !ok {
		return "", nil
	}
	for i := start + 1; i < end; i++ {
		name, value, ok := strings.Cut(strings.TrimSpace(lines[i]), "=")
		if !ok {
			continue
		}
		if strings.Trim(strings.TrimSpace(name), `"'`) == key {
			return unquoteTOMLString(value), nil
		}
	}
	return "", nil
}

// tomlTableEntry is one key/value row of a table.
type tomlTableEntry struct {
	Key   string
	Value string
}

// tableEntries reads a table's rows in file order. It is the multi-row
// companion to tableValue, used by the one-time thinking-level migration.
func tableEntries(lines []string, table string) []tomlTableEntry {
	start, end, ok := findTOMLTable(lines, table)
	if !ok {
		return nil
	}
	var entries []tomlTableEntry
	for i := start + 1; i < end; i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key := strings.Trim(strings.TrimSpace(name), `"'`)
		if key == "" {
			continue
		}
		entries = append(entries, tomlTableEntry{Key: key, Value: unquoteTOMLString(value)})
	}
	return entries
}

// findTOMLTable locates a table header and the line where its body ends (the
// next header, or EOF). The name avoids memoryrecall.go's findTable, which
// scans the same file with a different return shape.
func findTOMLTable(lines []string, table string) (start, end int, ok bool) {
	header := "[" + table + "]"
	for i, line := range lines {
		if strings.TrimSpace(line) != header {
			continue
		}
		end = len(lines)
		for j := i + 1; j < len(lines); j++ {
			if strings.HasPrefix(strings.TrimSpace(lines[j]), "[") {
				end = j
				break
			}
		}
		return i, end, true
	}
	return 0, 0, false
}

// setTableValue replaces, inserts or removes one key in a table. The table is
// created at the end of the file when it does not exist; a removal leaves the
// empty header in place, which TOML treats as an empty table.
func setTableValue(lines []string, table, key, value string) []string {
	start, end, ok := findTOMLTable(lines, table)
	if !ok {
		if value == "" {
			return lines
		}
		out := append([]string{}, lines...)
		if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
			out = append(out, "")
		}
		return append(out, "["+table+"]", "\t"+quoteTOMLKey(key)+" = "+quoteTOMLString(value))
	}
	for i := start + 1; i < end; i++ {
		name, _, ok := strings.Cut(strings.TrimSpace(lines[i]), "=")
		if !ok || strings.Trim(strings.TrimSpace(name), `"'`) != key {
			continue
		}
		out := append([]string{}, lines...)
		if value == "" {
			return append(out[:i], out[i+1:]...)
		}
		out[i] = "\t" + quoteTOMLKey(key) + " = " + quoteTOMLString(value)
		return out
	}
	if value == "" {
		return lines
	}
	out := append([]string{}, lines...)
	line := "\t" + quoteTOMLKey(key) + " = " + quoteTOMLString(value)
	return append(out[:end], append([]string{line}, out[end:]...)...)
}

// quoteTOMLKey leaves a bare key alone when it is valid TOML and quotes the
// rest, so provider names with unusual spelling cannot break the file.
func quoteTOMLKey(key string) string {
	if key == "" {
		return `""`
	}
	for _, r := range key {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return quoteTOMLString(key)
	}
	return key
}
