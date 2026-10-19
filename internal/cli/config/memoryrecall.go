package config

import (
	"fmt"
	"os"
	"strings"
)

// SetMemoryRecall writes `recall = <bool>` under [memory] in config.toml.
//
// Like the default-provider writer, this edits the two or three lines it owns
// instead of re-encoding the file: config.toml is hand-written, and a
// decode/encode round trip would drop every comment in it. The [memory] table is
// created when it is missing, and the key is inserted right after its header so
// the section reads as a unit.
func SetMemoryRecall(on bool) error {
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
	value := memoryBool(on)

	headerAt, bodyAt, bodyEnd, found := findTable(lines, memoryTable)
	switch {
	case !found:
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, "["+memoryTable+"]", memoryKey+" = "+value)
	case bodyAt > headerAt:
		// The key already exists: replace the line in place.
		lines[bodyAt-1] = memoryKey + " = " + value
	default:
		out := make([]string, 0, len(lines)+1)
		out = append(out, lines[:headerAt+1]...)
		out = append(out, memoryKey+" = "+value)
		out = append(out, lines[bodyEnd:]...)
		lines = out
	}

	result := strings.Join(lines, "\n")
	if hadTrailingNewline && result != "" {
		result += "\n"
	}
	if result == text {
		return nil
	}
	return writeConfigFile(path, result)
}

const (
	memoryTable = "memory"
	memoryKey   = "recall"
)

func memoryBool(on bool) string {
	if on {
		return "true"
	}
	return "false"
}

// findTable locates a TOML table header and, within it, an existing memoryKey
// line. headerAt is the header's index, keyAt the key's index (0 when absent),
// bodyEnd the index one past the table's last line.
func findTable(lines []string, table string) (headerAt, keyAt, bodyEnd int, found bool) {
	headerAt = -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "[") {
			continue
		}
		if headerAt >= 0 {
			// The previous table ends here.
			bodyEnd = i
			break
		}
		if strings.TrimSpace(strings.Trim(trimmed, "[]")) == table {
			headerAt = i
		}
	}
	if headerAt < 0 {
		return 0, 0, 0, false
	}
	if bodyEnd == 0 {
		bodyEnd = len(lines)
	}
	for i := headerAt + 1; i < bodyEnd; i++ {
		name, _, ok := strings.Cut(strings.TrimSpace(lines[i]), "=")
		if ok && strings.TrimSpace(name) == memoryKey {
			keyAt = i + 1
			break
		}
	}
	return headerAt, keyAt, bodyEnd, true
}
