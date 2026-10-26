package uv

import (
	"bytes"
	"context"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

// splitScan simulates an arbitrary read boundary followed by an expired Esc
// timer, without sleeping. Every split must have the same meaning as one read.
func splitScan(parts ...string) []Event {
	s := newEventScanner()
	var pending []byte
	var events []Event
	for _, part := range parts {
		pending = append(pending, part...)
		n, ev := s.scanEvents(pending, false)
		pending = pending[n:]
		events = append(events, ev...)
		// Recognized frames do not have an ambiguity timer in StreamEvents.
		// Still exercise an accidental stale timer on an unambiguous prefix.
		if len(pending) > 2 {
			n, ev = s.scanEvents(pending, true)
			pending = pending[n:]
			events = append(events, ev...)
		}
	}
	return events
}

func TestInputFramesAllSplitPositions(t *testing.T) {
	for name, seq := range map[string]string{
		"SGR mouse press":    "\x1b[<0;65;59M",
		"SGR mouse release":  "\x1b[<0;65;59m",
		"X10 mouse":          "\x1b[M !!",
		"cursor position":    "\x1b[?59;65R",
		"device attributes":  "\x1b[?1;2c",
		"keyboard flags":     "\x1b[?3u",
		"mode report":        "\x1b[?2026;1$y",
		"kitty key":          "\x1b[97;5u",
		"modified arrow":     "\x1b[1;5A",
		"focus in":           "\x1b[I",
		"focus out":          "\x1b[O",
		"SS3":                "\x1bOP",
		"foreground":         "\x1b]10;rgb:aaaa/bbbb/cccc\x07",
		"background":         "\x1b]11;rgb:0000/0000/0000\x1b\\",
		"cursor color":       "\x1b]12;rgb:ffff/ffff/ffff\x07",
		"clipboard":          "\x1b]52;c;aGVsbG8=\x1b\\",
		"DCS capability":     "\x1bP1+r544e=787465726d\x1b\\",
		"APC graphics reply": "\x1b_Gi=1;OK\x1b\\",
		"PM":                 "\x1b^private reply\x1b\\",
		"SOS":                "\x1bXreply\x1b\\",
		"C1 mouse":           "\x9b<0;65;59M",
		"C1 X10 mouse":       "\x9bM !!",
		"C1 background":      "\x9d11;rgb:0000/0000/0000\x9c",
		"Win32 Unicode":      "\x1b[0;0;97;1;0;1_",
		"Win32 UTF16":        "\x1b[0;0;55357;1;0;1_\x1b[0;0;56832;1;0;1_",
		"UTF8":               "你好🌍",
	} {
		t.Run(name, func(t *testing.T) {
			want := splitScan(seq)
			for i := 1; i < len(seq); i++ {
				got := splitScan(seq[:i], seq[i:])
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("split %d/%d: got %#v, want %#v", i, len(seq), got, want)
				}
			}
		})
	}
}

func TestInputPasteIsOpaqueAcrossEverySplit(t *testing.T) {
	payload := "65;59M 中文 \x03\n\x1b[<0;65;59M\x1b]11;rgb:0000/0000/0000\x07"
	seq := "\x1b[200~" + payload + "\x1b[201~after"
	for i := 1; i < len(seq); i++ {
		got := splitScan(seq[:i], seq[i:])
		var pastes []string
		var text strings.Builder
		for _, event := range got {
			switch e := event.(type) {
			case PasteEvent:
				pastes = append(pastes, e.Content)
			case KeyPressEvent:
				text.WriteString(e.Text)
			}
		}
		if !reflect.DeepEqual(pastes, []string{payload}) || text.String() != "after" {
			t.Fatalf("split %d: paste=%q text=%q", i, pastes, text.String())
		}
	}
}

func TestInputMalformedAndOversizeRecovery(t *testing.T) {
	for _, parts := range [][]string{
		{"\x1b[<0;", "\x03ok"},
		{"\x1b]11;rgb:", "\x1b[Aok"},
		{"\x1bP1+r", "\x04ok"},
		{"\x1b[<\xff65;", "59Mok"},
		{"\x1b[<" + strings.Repeat("1", maxInputSequence), ";65;59Mok"},
		{"\x1b]11;" + strings.Repeat("a", maxInputString), "tail\x07ok"},
		{"\x1bP" + strings.Repeat("a", maxInputString), "tail\x1b", "\\ok"},
		{"\x1bP" + strings.Repeat("a", maxInputString), "\x03ok"},
	} {
		var text strings.Builder
		for _, e := range splitScan(parts...) {
			if k, ok := e.(KeyPressEvent); ok {
				text.WriteString(k.Text)
			}
		}
		if text.String() != "ok" {
			t.Fatalf("bad frame tail leaked or recovery lost: text len=%d prefix=%q", text.Len(), text.String()[:min(text.Len(), 40)])
		}
	}
}

func TestInputLiteralReportLookingTextIsNotFiltered(t *testing.T) {
	const text = "65;59M 1;rgb:0000/0000/0000 [201~"
	var got strings.Builder
	for _, e := range splitScan(text) {
		if k, ok := e.(KeyPressEvent); ok {
			got.WriteString(k.Text)
		}
	}
	if got.String() != text {
		t.Fatalf("literal input changed: %q", got.String())
	}
}

func TestInputDelayedFramesDoNotExpire(t *testing.T) {
	for _, seq := range []string{"\x1b[<0;65;59M", "\x1b]11;rgb:0000/0000/0000\x1b\\", "\x1b[I"} {
		r, w := io.Pipe()
		events := make(chan Event, 128)
		d := NewTerminalReader(r, "xterm-256color")
		d.EscTimeout = 5 * time.Millisecond
		go func() {
			_, _ = io.WriteString(w, seq[:2])
			time.Sleep(30 * time.Millisecond)
			_, _ = io.WriteString(w, seq[2:]+"ok")
			_ = w.Close()
		}()
		if err := d.StreamEvents(context.Background(), events); err != nil {
			t.Fatal(err)
		}
		close(events)
		var text strings.Builder
		for e := range events {
			if k, ok := e.(KeyPressEvent); ok {
				text.WriteString(k.Text)
			}
		}
		_ = r.Close()
		if text.String() != "ok" {
			t.Fatalf("delayed %q produced text %q", seq, text.String())
		}
	}
}

type eofInput struct{ *bytes.Reader }

func (r eofInput) Read(p []byte) (int, error) {
	n, _ := r.Reader.Read(p)
	return n, io.EOF
}

func TestInputBytesAlongsideEOF(t *testing.T) {
	d := NewTerminalReader(eofInput{bytes.NewReader([]byte("last"))}, "dumb")
	events := make(chan Event, 16)
	if err := d.StreamEvents(context.Background(), events); err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 {
		t.Fatalf("last read was lost: %d events", len(events))
	}
}

func TestInputBareEscapeAndCancellation(t *testing.T) {
	r, w := io.Pipe()
	defer r.Close()
	defer w.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := NewTerminalReader(r, "dumb")
	d.EscTimeout = 5 * time.Millisecond
	events := make(chan Event)
	done := make(chan error, 1)
	go func() { done <- d.StreamEvents(ctx, events) }()
	_, _ = io.WriteString(w, "\x1b")
	select {
	case e := <-events:
		if k, ok := e.(KeyPressEvent); !ok || k.Code != KeyEscape {
			t.Fatalf("bare Esc = %#v", e)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("bare Esc became a slow sequence timeout")
	}
	_, _ = io.WriteString(w, "blocked consumer")
	cancel()
	_ = w.Close() // io.Pipe has no Cancel method
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancellation blocked while emitting events")
	}
}
