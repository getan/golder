package tui

import (
	"fmt"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

func TestScratchPgTildeForms(t *testing.T) {
	for _, raw := range []string{
		"\x1b[5~", "\x1b[6~",
		"\x1b[5;1~", "\x1b[6;1~",
		"\x1b[5;1:1~", "\x1b[6;1:1~",
	} {
		var dec uv.EventDecoder
		_, ev := dec.Decode([]byte(raw))
		kp, ok := ev.(uv.KeyPressEvent)
		if !ok {
			fmt.Printf("%-14q -> %T\n", raw, ev)
			continue
		}
		msg := tea.KeyPressMsg(kp)
		fmt.Printf("%-14q String()=%-12q Keystroke()=%-12q Code=%v\n", raw, msg.String(), msg.Keystroke(), msg.Code)
	}
}
