package agenttool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func longJSXLine(size int) string {
	prefix := `const Workspace = () => <Card><Button onClick={()=>void save(true)} data-label="`
	suffix := `">{index<total-1?"保存并下一条":"保存最后一条"}</Button></Card>;`
	return prefix + strings.Repeat("x", size-len(prefix)-len(suffix)) + suffix
}

func TestReadOutputIsUsableAsWholeLinePatchContext(t *testing.T) {
	for _, size := range []int{2094, 2804, 28_040, readToolMaxLineBytes} {
		for _, kind := range []string{"context", "delete"} {
			t.Run(fmt.Sprintf("%d/%s", size, kind), func(t *testing.T) {
				dir := t.TempDir()
				line := longJSXLine(size)
				original := strings.Repeat("// earlier line\n", 99) + line + "\n"
				path := filepath.Join(dir, "AudioTaskWorkspace.tsx")
				if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
					t.Fatal(err)
				}
				args := map[string]any{"path": "AudioTaskWorkspace.tsx", "offset": 100, "limit": 1}
				if size > readToolMaxLineLen {
					args["max_line_bytes"] = size
				}
				read, _ := runRead(t, &ReadTool{Root: dir}, args)
				_, body, ok := strings.Cut(strings.TrimSuffix(resultText(read), "\n"), "\t")
				if read.IsError || !ok || body != line {
					t.Fatalf("numbered read did not return the complete %d-byte line", size)
				}
				hunk := " " + body + "\n+// following note\n"
				want := original + "// following note\n"
				if kind == "delete" {
					updated := strings.Replace(body, "save(true)", "save(false)", 1)
					hunk = "-" + body + "\n+" + updated + "\n"
					want = strings.Repeat("// earlier line\n", 99) + updated + "\n"
				}
				result := runPatch(t, &ApplyPatchTool{Root: dir}, "*** Begin Patch\n*** Update File: AudioTaskWorkspace.tsx\n@@\n"+hunk+"*** End Patch\n")
				if result.IsError {
					t.Fatalf("read -> apply_patch failed: %s", resultText(result))
				}
				got, err := os.ReadFile(path)
				if err != nil || string(got) != want {
					t.Fatalf("patch did not produce the expected file: %v", err)
				}
			})
		}
	}
}

func TestReadLongLineReportsActualBytesAndSafeUTF8Boundary(t *testing.T) {
	for _, tc := range []struct {
		name  string
		line  string
		shown int
	}{
		{"ascii", longJSXLine(28_040), readToolMaxLineLen},
		{"utf8", strings.Repeat("a", readToolMaxLineLen-1) + strings.Repeat("保存", 2000), readToolMaxLineLen - 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text, _ := readNumbered(strings.NewReader(tc.line+"\n"), 1, 1, 0)
			if !utf8.ValidString(text) {
				t.Fatal("read split a UTF-8 rune")
			}
			want := fmt.Sprintf("line truncated: showing %d of %d bytes", tc.shown, len(tc.line))
			if !strings.Contains(text, want) || !strings.Contains(text, fmt.Sprintf("max_line_bytes=%d", len(tc.line))) || !strings.Contains(text, "preview is not patch context") {
				t.Fatalf("missing exact size or actionable recovery: %s", text[tc.shown:])
			}
		})
	}
}

func TestReadOutputBudgetPaginatesCompleteLines(t *testing.T) {
	line := strings.Repeat("x", readToolMaxLineLen)
	input := strings.Repeat(line+"\n", 20)
	text, truncated := readNumbered(strings.NewReader(input), 1, 0, 0)
	if !truncated || len(text) > readToolMaxBytes || !strings.Contains(text, "100000-byte output budget") || !strings.Contains(text, "resume with offset=7") {
		t.Fatalf("unbounded or misleading page: %d bytes, tail=%q", len(text), text[max(0, len(text)-240):])
	}
	if strings.Contains(text, "     7\t") || strings.Contains(text, "line truncated") {
		t.Fatal("output budget emitted a partial source line")
	}
	next, _ := readNumbered(strings.NewReader(input), 7, 1, 0)
	if !strings.HasPrefix(next, "     7\t"+line+"\n") {
		t.Fatal("resume offset skipped or changed a line")
	}
}

func TestReadMaximumOverrideStillFitsBudget(t *testing.T) {
	line := strings.Repeat("x", readToolMaxLineBytes)
	text, _ := readNumbered(strings.NewReader(line+"\n"), 1, 1, readToolMaxLineBytes)
	if text != "     1\t"+line+"\n" || len(text) > readToolMaxBytes {
		t.Fatal("maximum supported source line could not be read in full")
	}
	text, _ = readNumbered(strings.NewReader(line+"x\n"), 1, 1, readToolMaxLineBytes)
	if !strings.Contains(text, "exceeds max_line_bytes maximum 98304") || strings.Contains(text, "max_line_bytes=98305") {
		t.Fatal("oversized line recommended an impossible reread")
	}
}

func TestReadRejectsInvalidMaxLineBytes(t *testing.T) {
	for _, size := range []int{-1, readToolMaxLineBytes + 1} {
		result, _ := runRead(t, &ReadTool{Root: t.TempDir()}, map[string]any{"path": "not-opened", "max_line_bytes": size})
		if !result.IsError || !strings.Contains(resultText(result), "max_line_bytes must be between") {
			t.Fatalf("invalid line budget was not rejected: %s", resultText(result))
		}
	}
}
