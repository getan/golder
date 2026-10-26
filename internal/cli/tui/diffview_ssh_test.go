package tui

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Skip RGB/indexed foreground operands so a color component such as 48 is not
// mistaken for the background SGR command with the same number.
func diffHasBackgroundSGR(out string) bool {
	for _, seq := range regexp.MustCompile("\x1b\\[[0-9;]*m").FindAllString(out, -1) {
		params := strings.Split(seq[2:len(seq)-1], ";")
		for i := 0; i < len(params); i++ {
			n, _ := strconv.Atoi(params[i])
			if n == 48 || n >= 40 && n <= 47 || n >= 100 && n <= 107 {
				return true
			}
			if (n == 38 || n == 58) && i+1 < len(params) {
				switch params[i+1] {
				case "2":
					i += 4
				case "5":
					i += 2
				}
			}
		}
	}
	return false
}

func TestApplyPatchSSHOmitsOnlyDiffBackground(t *testing.T) {
	palette := syntaxDark()
	t.Cleanup(func() { SetMarkdownDark(palette) })
	for _, dark := range []bool{true, false} {
		for _, sshVar := range []string{"SSH_TTY", "SSH_CONNECTION"} {
			t.Run(fmt.Sprintf("dark=%v/%s", dark, sshVar), func(t *testing.T) {
				clearMotionEnv(t)
				SetMarkdownDark(dark)
				// Reduced motion by itself must NOT change the local diff palette.
				t.Setenv("GOLDER_NO_ANIMATIONS", "1")
				const code = "func newValue() int { return 42 }"
				const diff = "--- a/ssh.go\n+++ b/ssh.go\n@@ -1 +1 @@\n-func oldValue() int { return 1 }\n+" + code + "\n"
				card := toolCard{name: "apply_patch", state: cardSuccess, expanded: true}
				card.complete(true, "Applied 1 change(s):\n  U ssh.go", map[string]any{"diff": diff})
				theme := DefaultTheme()
				local := card.render(theme, 80)
				if !diffHasBackgroundSGR(local) {
					t.Fatal("local diff must retain its original background wash")
				}
				t.Setenv(sshVar, "ssh-test")
				remote := card.render(theme, 80) // identical diff/width/palette: cache must respect SSH
				if diffHasBackgroundSGR(remote) {
					t.Fatalf("SSH diff still paints a background: %q", remote)
				}
				for _, sign := range []string{theme.DiffAdd.Render("+"), theme.DiffDel.Render("-")} {
					if !strings.Contains(remote, sign) {
						t.Fatalf("SSH changed the colored + or - marker: %q", remote)
					}
				}
				colored := 0
				for _, span := range highlightCodeLine("ssh.go", code, dark) {
					if span.color != "" && strings.TrimSpace(span.text) != "" {
						colored++
						if !strings.Contains(remote, renderHLSpans([]hlSpan{span}, theme.ToolCmd.Background(nil))) {
							t.Fatalf("SSH lost syntax color for %q", span.text)
						}
					}
				}
				if colored == 0 {
					t.Fatal("fixture must exercise code syntax highlighting")
				}
				if stripTCardANSI(remote) != stripTCardANSI(local) {
					t.Fatal("SSH changed diff text, markers, indentation or wrapping")
				}
				if cached := card.render(theme, 80); cached != remote {
					t.Fatal("SSH diff cache changed the rendered result")
				}
				t.Setenv(sshVar, "")
				if got := card.render(theme, 80); got != local {
					t.Fatal("SSH rendering polluted the local diff cache")
				}
			})
		}
	}
}
