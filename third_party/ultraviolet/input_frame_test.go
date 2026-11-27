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

// A report can lose its ESC across a read boundary: the ESC arrives first,
// the introducer and payload follow after the ordinary Esc timeout. The ESC
// must stay pending, join the introducer and decode as the original report —
// not become an Escape key followed by the report's text ("0;14;40m").
func TestInputSplitEscapeJoinsLateProtocolTail(t *testing.T) {
	r, w := io.Pipe()
	events := make(chan Event, 16)
	d := NewTerminalReader(r, "xterm-256color")
	d.EscTimeout = 5 * time.Millisecond
	d.BareEscTimeout = 100 * time.Millisecond
	go func() {
		_, _ = io.WriteString(w, "\x1b")
		time.Sleep(25 * time.Millisecond)
		_, _ = io.WriteString(w, "[<0;14;40m")
		_ = w.Close()
	}()
	if err := d.StreamEvents(context.Background(), events); err != nil {
		t.Fatal(err)
	}
	close(events)
	var text strings.Builder
	var escape, release bool
	for e := range events {
		switch e := e.(type) {
		case KeyPressEvent:
			if e.Code == KeyEscape {
				escape = true
				continue
			}
			text.WriteString(e.Text)
		case MouseReleaseEvent:
			release = true
		}
	}
	if text.Len() != 0 {
		t.Fatalf("late SGR mouse tail became text: %q", text.String())
	}
	if escape {
		t.Fatal("the split ESC was also delivered as an Escape key")
	}
	if !release {
		t.Fatal("the late SGR mouse release was not decoded")
	}
}

// Tab is the key the reported SSH failure was typing when the sequence split:
// a kitty CSI-u report must arrive as Tab, not as Escape plus "9;1u".
func TestInputSplitEscapeJoinsKittyTab(t *testing.T) {
	r, w := io.Pipe()
	events := make(chan Event, 16)
	d := NewTerminalReader(r, "xterm-256color")
	d.EscTimeout = 5 * time.Millisecond
	d.BareEscTimeout = 100 * time.Millisecond
	go func() {
		_, _ = io.WriteString(w, "\x1b")
		time.Sleep(25 * time.Millisecond)
		_, _ = io.WriteString(w, "[9;1u")
		_ = w.Close()
	}()
	if err := d.StreamEvents(context.Background(), events); err != nil {
		t.Fatal(err)
	}
	close(events)
	var text strings.Builder
	var tab, escape bool
	for e := range events {
		if k, ok := e.(KeyPressEvent); ok {
			switch k.Code {
			case KeyTab:
				tab = true
			case KeyEscape:
				escape = true
			default:
				text.WriteString(k.Text)
			}
		}
	}
	if escape || text.Len() != 0 {
		t.Fatalf("split kitty Tab became escape=%t text=%q", escape, text.String())
	}
	if !tab {
		t.Fatal("the split kitty Tab report did not decode as Tab")
	}
}

// The pending ESC must not delay a following ordinary key: it is delivered
// before that key, and the key itself is still typed.
func TestInputSplitEscapeBeforeTextKeepsOrder(t *testing.T) {
	r, w := io.Pipe()
	events := make(chan Event, 16)
	d := NewTerminalReader(r, "xterm-256color")
	d.EscTimeout = 5 * time.Millisecond
	d.BareEscTimeout = 100 * time.Millisecond
	go func() {
		_, _ = io.WriteString(w, "\x1b")
		time.Sleep(25 * time.Millisecond)
		_, _ = io.WriteString(w, "a")
		_ = w.Close()
	}()
	if err := d.StreamEvents(context.Background(), events); err != nil {
		t.Fatal(err)
	}
	close(events)
	var got []string
	for e := range events {
		if k, ok := e.(KeyPressEvent); ok {
			got = append(got, k.String())
		}
	}
	if !reflect.DeepEqual(got, []string{"esc", "a"}) {
		t.Fatalf("split Esc then a = %v, want [esc a]", got)
	}
}

// An isolated Esc is still delivered, after the extra grace period, rather
// than being held forever waiting for a report that never comes.
func TestInputLoneEscapeStillArrives(t *testing.T) {
	r, w := io.Pipe()
	events := make(chan Event, 8)
	d := NewTerminalReader(r, "xterm-256color")
	d.EscTimeout = 5 * time.Millisecond
	d.BareEscTimeout = 20 * time.Millisecond
	go func() {
		_, _ = io.WriteString(w, "\x1b")
		time.Sleep(60 * time.Millisecond)
		_ = w.Close()
	}()
	if err := d.StreamEvents(context.Background(), events); err != nil {
		t.Fatal(err)
	}
	close(events)
	var escape bool
	for e := range events {
		if k, ok := e.(KeyPressEvent); ok && k.Code == KeyEscape {
			escape = true
		}
	}
	if !escape {
		t.Fatal("a lone Esc was not delivered after the grace period")
	}
}

// A tail can arrive after BareEscTimeout as well. The Escape is already out,
// but the tail must still be decoded as its report, never typed as text.
func TestInputVeryLateProtocolTailIsNotTyped(t *testing.T) {
	r, w := io.Pipe()
	events := make(chan Event, 16)
	d := NewTerminalReader(r, "xterm-256color")
	d.EscTimeout = 5 * time.Millisecond
	d.BareEscTimeout = 20 * time.Millisecond
	d.EscTailWindow = 500 * time.Millisecond
	go func() {
		_, _ = io.WriteString(w, "\x1b")
		time.Sleep(50 * time.Millisecond)
		_, _ = io.WriteString(w, "[<0;14;40m")
		_ = w.Close()
	}()
	if err := d.StreamEvents(context.Background(), events); err != nil {
		t.Fatal(err)
	}
	close(events)
	var text strings.Builder
	var release bool
	for e := range events {
		switch e := e.(type) {
		case KeyPressEvent:
			text.WriteString(e.Text)
		case MouseReleaseEvent:
			release = true
		}
	}
	if text.Len() != 0 {
		t.Fatalf("very late SGR mouse tail became text: %q", text.String())
	}
	if !release {
		t.Fatal("the very late SGR mouse release was not decoded")
	}
}

// A recognized report prefix that never completes is cancelled by the next
// ESC. The discarded prefix must produce a neutral event so the host can
// clear a pending quit arm before it sees that second Escape.
func TestInputCancelledFrameSignalsDroppedReport(t *testing.T) {
	r, w := io.Pipe()
	events := make(chan Event, 16)
	d := NewTerminalReader(r, "xterm-256color")
	d.EscTimeout = 5 * time.Millisecond
	d.BareEscTimeout = 20 * time.Millisecond
	go func() {
		_, _ = io.WriteString(w, "\x1b")
		time.Sleep(50 * time.Millisecond) // first Esc is delivered
		_, _ = io.WriteString(w, "[<0;")
		time.Sleep(20 * time.Millisecond) // recognized-but-unfinished report
		_, _ = io.WriteString(w, "\x1b")
		_ = w.Close()
	}()
	if err := d.StreamEvents(context.Background(), events); err != nil {
		t.Fatal(err)
	}
	close(events)
	var sequence []string
	for e := range events {
		switch e := e.(type) {
		case KeyPressEvent:
			if e.Code == KeyEscape {
				sequence = append(sequence, "esc")
			} else {
				sequence = append(sequence, "key:"+e.Text)
			}
		case UnknownEvent:
			sequence = append(sequence, "dropped")
		}
	}
	want := []string{"esc", "dropped", "esc"}
	if !reflect.DeepEqual(sequence, want) {
		t.Fatalf("cancelled report sequence = %v, want %v", sequence, want)
	}
}

// A bare CSI prefix cancelled by the next Esc is the acceptance case: the
// first Esc armed a quit, the malformed "ESC[" must clear it, and the second
// Esc may only arm again.
func TestInputCancelledBareCSISignalsDroppedReport(t *testing.T) {
	r, w := io.Pipe()
	events := make(chan Event, 16)
	d := NewTerminalReader(r, "xterm-256color")
	d.EscTimeout = 5 * time.Millisecond
	d.BareEscTimeout = 20 * time.Millisecond
	go func() {
		_, _ = io.WriteString(w, "\x1b")
		time.Sleep(50 * time.Millisecond) // first Esc is delivered
		_, _ = io.WriteString(w, "\x1b[")
		time.Sleep(20 * time.Millisecond)
		_, _ = io.WriteString(w, "\x1b")
		_ = w.Close()
	}()
	if err := d.StreamEvents(context.Background(), events); err != nil {
		t.Fatal(err)
	}
	close(events)
	var sequence []string
	for e := range events {
		switch e := e.(type) {
		case KeyPressEvent:
			if e.Code == KeyEscape {
				sequence = append(sequence, "esc")
			}
		case UnknownEvent:
			sequence = append(sequence, "dropped")
		}
	}
	want := []string{"esc", "dropped", "esc"}
	if !reflect.DeepEqual(sequence, want) {
		t.Fatalf("cancelled bare CSI sequence = %v, want %v", sequence, want)
	}
}

// A literal "[" typed after a real Escape is not a report. The recognizer is
// narrower than the frame introducer for exactly this case.
func TestInputEscapeThenTypedBracketStaysText(t *testing.T) {
	r, w := io.Pipe()
	events := make(chan Event, 16)
	d := NewTerminalReader(r, "xterm-256color")
	d.EscTimeout = 5 * time.Millisecond
	d.BareEscTimeout = 20 * time.Millisecond
	go func() {
		_, _ = io.WriteString(w, "\x1b")
		time.Sleep(50 * time.Millisecond)
		_, _ = io.WriteString(w, "[x")
		_ = w.Close()
	}()
	if err := d.StreamEvents(context.Background(), events); err != nil {
		t.Fatal(err)
	}
	close(events)
	var text strings.Builder
	var escape bool
	for e := range events {
		if k, ok := e.(KeyPressEvent); ok {
			if k.Code == KeyEscape {
				escape = true
				continue
			}
			text.WriteString(k.Text)
		}
	}
	if !escape || text.String() != "[x" {
		t.Fatalf("Esc then typed [x = escape=%t text=%q, want escape and [x", escape, text.String())
	}
}

// During the Esc grace window, bytes that could also begin a protocol frame
// but do not identify a report must remain ordinary typing. Only report
// prefixes may join a pending Escape.
func TestInputHeldEscapeDoesNotSwallowTypedInput(t *testing.T) {
	for _, typed := range []string{"P", "_", "^", "X", "[", "[x", "O", "Ox"} {
		t.Run(typed, func(t *testing.T) {
			r, w := io.Pipe()
			events := make(chan Event, 16)
			d := NewTerminalReader(r, "xterm-256color")
			d.EscTimeout = 5 * time.Millisecond
			d.BareEscTimeout = 100 * time.Millisecond
			go func() {
				_, _ = io.WriteString(w, "\x1b")
				time.Sleep(25 * time.Millisecond)
				_, _ = io.WriteString(w, typed)
				_ = w.Close()
			}()
			if err := d.StreamEvents(context.Background(), events); err != nil {
				t.Fatal(err)
			}
			close(events)
			var got strings.Builder
			var escape bool
			for e := range events {
				if k, ok := e.(KeyPressEvent); ok {
					if k.Code == KeyEscape {
						escape = true
						continue
					}
					got.WriteString(k.Text)
				}
			}
			if !escape || got.String() != typed {
				t.Fatalf("Esc then typed %q = escape=%t text=%q", typed, escape, got.String())
			}
		})
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
	// This fixture is about a bare Esc, not the report-joining grace period.
	d.BareEscTimeout = 20 * time.Millisecond
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
