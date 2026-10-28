package repl

import (
	"bufio"
	"bytes"
	"strings"
	"testing"

	"github.com/getan/golder/internal/runtime"
)

func TestStandaloneREPLProTabDoesNotSubmitOrReturnEOF(t *testing.T) {
	reg := runtime.NewSlashRegistry()
	reg.AddBuiltin(runtime.SlashCommand{Name: "provider"})
	reg.AddBuiltin(runtime.SlashCommand{Name: "proxy"})
	var output bytes.Buffer
	input := strings.NewReader("/pro\t suffix\r")
	editor := newREPLLineEditor(input, bufio.NewReader(input), &output, reg, nil)
	got, err := editor.editLoop("> ")
	if err != nil || got != "/provider suffix" {
		t.Fatalf("Tab exited/submitted instead of editing: text=%q err=%v", got, err)
	}
}
