package uv

import (
	"bytes"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestSyncFlushHidesPaintingCursorAndRestoresFinalState(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cursor  *Cursor
		visible bool
	}{
		{"visible", &Cursor{Position: Position{X: 2, Y: 18}}, true},
		{"hidden", &Cursor{Hidden: true}, false},
		{"nil", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			s := NewTerminalScreen(&out, nil)
			s.syncUpdates, s.altScreen, s.cursor = true, true, tc.cursor
			// Include a premature show, as SetCursor can enqueue one before
			// Render. It must not make a mid-body cursor visible.
			s.buf.WriteString(ansi.ShowCursor + "\x1b[5;10H正文🙂")
			if err := s.Flush(); err != nil {
				t.Fatal(err)
			}
			got := out.String()
			if !strings.HasPrefix(got, ansi.SetModeSynchronizedOutput+ansi.HideCursor) {
				t.Fatalf("paint cursor not hidden inside the sync transaction: %q", got)
			}
			if !strings.HasSuffix(got, ansi.ResetModeSynchronizedOutput) {
				t.Fatalf("sync transaction not closed: %q", got)
			}
			n := strings.Count(got, ansi.ShowCursor)
			if tc.visible {
				if n != 1 || !strings.HasSuffix(got, ansi.ShowCursor+ansi.ResetModeSynchronizedOutput) {
					t.Fatalf("final visible cursor not restored after painting: %q", got)
				}
			} else if n != 0 {
				t.Fatalf("hidden/nil final cursor was made visible: %q", got)
			}
		})
	}
}
