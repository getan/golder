package uv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/cancelreader"
	"github.com/rivo/uniseg"
)

// ErrReaderNotStarted is returned when the reader has not been started yet.
var ErrReaderNotStarted = fmt.Errorf("reader not started")

// DefaultEscTimeout is the default timeout at which the [TerminalReader] will
// process ESC sequences. It is set to 50 milliseconds.
const DefaultEscTimeout = 50 * time.Millisecond

// TerminalReader represents an input event loop that reads input events from
// a reader and parses them into human-readable events. It supports
// reading escape sequences, mouse events, and bracketed paste mode.
type TerminalReader struct {
	EventDecoder

	// MouseMode determines whether mouse events are enabled or not. This is a
	// platform-specific feature and is only available on Windows. When this is
	// true, the reader will be initialized to read mouse events using the
	// Windows Console API.
	MouseMode *MouseMode

	// EscTimeout is the escape character timeout duration. Most escape
	// sequences start with an escape character [ansi.ESC] and are followed by
	// one or more characters. If the next character is not received within
	// this timeout, the reader will assume that the escape sequence is
	// complete and will process the received characters as a complete escape
	// sequence.
	//
	// By default, this is set to [DefaultEscTimeout] (50 milliseconds).
	EscTimeout time.Duration

	r     io.Reader
	table map[string]Key // table is a lookup table for key sequences.

	term string // term is the terminal name $TERM.

	// paste is the bracketed paste mode buffer.
	// When nil, bracketed paste mode is disabled.
	paste []byte

	lookup bool // lookup indicates whether to use the lookup table for key sequences.

	// vtInput indicates whether we're using Windows Console API VT input mode.
	//nolint:unused,nolintlint
	vtInput bool

	eventScanner *eventScanner

	// We use these buffers to decode UTF-16 sequences and graphemes from the
	// Windows Console API and Win32-Input-Mode events.
	utf16Half   [2]bool    // 0 key up, 1 key down
	utf16Buf    [2][2]rune // 0 key up, 1 key down
	graphemeBuf [2][]rune  // 0 key up, 1 key down
	//nolint:unused,nolintlint
	lastMouseBtns uint32 // the last mouse button state for the previous event
	//nolint:unused,nolintlint
	lastWinsizeX, lastWinsizeY int16 // the last window size for the previous event to prevent multiple size events from firing

	logger Logger // The logger to use for debugging.
}

// NewTerminalReader returns a new input event reader. The reader streams input
// events from the terminal and parses escape sequences into human-readable
// events. It supports reading Terminfo databases.
//
// Use [TerminalReader.UseTerminfo] to use Terminfo defined key sequences.
// Use [TerminalReader.Legacy] to control legacy key encoding behavior.
//
// Example:
//
//	```go
//	var cr cancelreader.CancelReader
//	var evc chan Event
//	sc := NewTerminalReader(cr, os.Getenv("TERM"))
//	go sc.StreamEvents(ctx, evc)
//	```
func NewTerminalReader(r io.Reader, termType string) *TerminalReader {
	d := &TerminalReader{
		EscTimeout: DefaultEscTimeout,
		r:          r,
		term:       termType,
		lookup:     true, // Use lookup table by default.
	}
	d.r = r
	if d.table == nil {
		d.table = buildKeysTable(d.Legacy, d.term, d.UseTerminfo)
	}
	evs := newEventScanner()
	evs.EventDecoder = d.EventDecoder
	evs.table = d.table
	evs.lookup = d.lookup
	evs.setLogger(d.logger)
	d.eventScanner = evs
	return d
}

// readBufSize is the size of the read buffer used to read input events at a time.
const readBufSize = 4096

// sendBytes reads data from the reader and sends it to the provided channel.
// It stops when an error occurs or when the context is closed.
func (d *TerminalReader) sendBytes(ctx context.Context, readc chan []byte) error {
	for {
		var readBuf [readBufSize]byte
		n, err := d.r.Read(readBuf[:])
		if n > 0 {
			select {
			case <-ctx.Done():
				return nil
			case readc <- readBuf[:n]:
			}
		}
		if err != nil {
			return err //nolint:wrapcheck
		}
	}
}

// StreamEvents sends events to the provided channel. It stops when the context
// is closed or when an error occurs.
func (d *TerminalReader) StreamEvents(ctx context.Context, eventc chan<- Event) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var buf bytes.Buffer
	errc := make(chan error, 1)
	readc := make(chan []byte)
	timeout := time.NewTimer(d.EscTimeout)
	timeout.Stop()
	defer timeout.Stop()
	arm := func() {
		timeout.Stop()
		if wait := d.inputWait(buf.Bytes()); wait > 0 {
			timeout.Reset(wait)
		}
	}

	var wg sync.WaitGroup
	wg.Add(1)
	defer func() {
		cancel()
		if r, ok := d.r.(cancelreader.CancelReader); ok {
			r.Cancel()
		}
		wg.Wait()
	}()

	go func() {
		defer wg.Done()
		if err := d.streamData(ctx, readc); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, cancelreader.ErrCanceled) {
				errc <- nil
				return
			}
			errc <- err
			return
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-errc:
			d.sendEvents(ctx, eventc, buf.Bytes(), true)
			return err // return the first error encountered
		case <-timeout.C:
			// Only ambiguous keys expire. A recognized protocol continuation
			// has no timer and the scanner will never turn its tail into keys.
			buf.Next(d.sendEvents(ctx, eventc, buf.Bytes(), true))
			arm()
		case read := <-readc:
			d.logf("input: %q", read)
			buf.Write(read)
			buf.Next(d.sendEvents(ctx, eventc, buf.Bytes(), false))
			arm()
		}
	}
}

// SetLogger sets the logger to use for debugging. If nil, no logging will be
// performed.
func (d *TerminalReader) SetLogger(logger Logger) {
	d.logger = logger
}

func (d *TerminalReader) sendEvents(ctx context.Context, eventc chan<- Event, buf []byte, expired bool) int {
	n, events := d.eventScanner.scanEvents(buf, expired)
	for _, event := range events {
		select {
		case eventc <- event:
		case <-ctx.Done():
			return n
		}
	}
	return n
}

// eventScanner scans the buffer for events and sends them to the event channel.
type eventScanner struct {
	EventDecoder

	utf16Half     [2]bool    // 0 key up, 1 key down
	utf16Buf      [2][2]rune // 0 key up, 1 key down
	graphemeBuf   [2][]rune  // 0 key up, 1 key down
	paste         []byte
	pasteOverflow bool
	discard       inputFrameKind
	table         map[string]Key
	lookup        bool
	logger        Logger
}

// newEventScanner creates a new event scanner.
func newEventScanner() *eventScanner {
	return &eventScanner{}
}

// setLogger sets the logger to use for debugging. If nil, no logging will be
// performed.
func (d *eventScanner) setLogger(logger Logger) {
	d.logger = logger
}

func (d *eventScanner) logf(format string, v ...interface{}) {
	logf(d.logger, format, v...)
}

func (d *eventScanner) scanEvents(buf []byte, expired bool) (total int, events []Event) {
	if len(buf) == 0 {
		return 0, nil
	}

	d.logf("processing buf %d %q", len(buf), buf)

	// Lookup table first
	if d.lookup && d.paste == nil && d.discard == inputPlain && len(buf) > 2 && buf[0] == ansi.ESC {
		if k, ok := d.table[string(buf)]; ok {
			return len(buf), []Event{KeyPressEvent(k)}
		}
	}

	// total is the total number of bytes processed
	for len(buf) > 0 {
		if d.discard != inputPlain {
			n, done := discardInput(buf, d.discard)
			buf, total = buf[n:], total+n
			if done {
				d.discard = inputPlain
			} else {
				return total, events
			}
			continue
		}
		if d.paste != nil {
			n, ended := d.consumePaste(buf)
			buf, total = buf[n:], total+n
			if !ended {
				return total, events
			}
			if !d.pasteOverflow {
				events = append(events, PasteEvent{strings.ToValidUTF8(string(d.paste), "")})
			}
			events = append(events, PasteEndEvent{})
			d.paste, d.pasteOverflow = nil, false
			continue
		}
		f := frameInput(buf)
		if f.cancel {
			buf, total = buf[f.end:], total+f.end
			continue
		}
		if f.kind != inputPlain {
			if !f.complete {
				if len(buf) > f.limit() {
					// Keep only a possible split string terminator. Never expose
					// later payload bytes as keyboard input after a size limit.
					n := len(buf)
					if buf[n-1] == ansi.ESC {
						n--
					}
					d.discard = f.kind
					buf, total = buf[n:], total+n
					continue
				}
				if !(expired && len(buf) == 2 && buf[0] == ansi.ESC) {
					return total, events
				}
			} else if f.end > f.limit() {
				buf, total = buf[f.end:], total+f.end
				continue
			}
		} else if !utf8.FullRune(buf) {
			return total, events
		}
		if f.kind == inputCSI && f.complete && buf[f.end-1] == '_' {
			// Transform only a complete Win32 record, and count SOURCE bytes.
			// Preprocessing a whole read mixed transformed and original lengths
			// (e.g. CSI _ could expand two source bytes into eleven consumed bytes).
			if consumed, converted := d.deserializeWin32Input(buf[:f.end]); consumed > 0 {
				_, convertedEvents := d.scanEvents(converted, true)
				events = append(events, convertedEvents...)
				buf, total = buf[f.end:], total+f.end
				continue
			}
		}
		esc := buf[0] == ansi.ESC
		n, event := d.Decode(buf)
		if f.complete && n < f.end {
			// An unsupported/malformed frame is still one frame. In particular,
			// an invalid parameter byte must not release the suffix as typing.
			n, event = f.end, UnknownEvent(string(buf[:f.end]))
		}
		if n <= 0 {
			return total, events // incomplete decoder input must not spin
		}

		var isUnknown bool
		switch event.(type) {
		case ignoredEvent:
			// ignore this event
			event = nil
		case UnknownEvent:
			isUnknown = true
			// Try to look up the event in the table.
			if !expired && !(f.kind != inputPlain && f.complete) {
				return total, events
			}

			if k, ok := d.table[string(buf[:n])]; ok {
				events = append(events, KeyPressEvent(k))
				return total + n, events
			}

			events = append(events, event)
		case PasteStartEvent:
			d.paste = []byte{} // reset the paste buffer
			d.pasteOverflow = false
		}

		if !isUnknown && event != nil {
			if esc && n <= 2 && !expired {
				// Wait for more input
				return total, events
			}

			if m, ok := event.(MultiEvent); ok {
				// If the event is a MultiEvent, append all events to the queue.
				events = append(events, m...)
			} else {
				// Otherwise, just append the event to the queue.
				events = append(events, event)
			}
		}

		buf = buf[n:]
		total += n
	}

	return total, events
}

func (d *eventScanner) encodeGraphemeBufs() []byte {
	var b []byte
	for kd := range d.graphemeBuf {
		if len(d.graphemeBuf[kd]) > 0 {
			switch kd {
			case 1:
				b = append(b, string(d.graphemeBuf[kd])...)
			case 0:
				// Encode the release grapheme as Kitty Keyboard to get the release event.
				grs := uniseg.NewGraphemes(string(d.graphemeBuf[kd]))
				for grs.Next() {
					var codepoints string
					gr := grs.Str()
					for i, r := range gr {
						if r == 0 {
							continue
						}
						if i > 0 {
							codepoints += ":"
						}
						codepoints += strconv.FormatInt(int64(r), 10)
					}
					// This is dark :)
					// During serializing/deserializing of win32 input events, the
					// API will split a grapheme into runes and send them as a
					// keydown/keyup sequences. We collect the runes, decoded them
					// from UTF-16 just fine. However, [EventDecoder.Decode] will
					// always decode graphemes as [KeyPressEvent]s. Thus, to
					// workaround that and the existing API, while we are
					// intercepting win32 input events, we encode the grapheme
					// release events as Kitty Keyboard sequences so that
					// [EventDecoder.Decode] can properly decode them as
					// [KeyReleaseEvent]s.
					seq := fmt.Sprintf("\x1b[%d;1:3;%su", d.graphemeBuf[kd][0], codepoints)
					b = append(b, seq...)
				}
			}
			d.graphemeBuf[kd] = d.graphemeBuf[kd][:0] // reset the buffer
		}
	}
	return b
}

func (d *eventScanner) storeGraphemeRune(kd int, r rune) {
	if d.utf16Half[kd] {
		// We have a half pair that needs to be decoded.
		d.utf16Half[kd] = false
		d.utf16Buf[kd][1] = r
		r := utf16.DecodeRune(d.utf16Buf[kd][0], d.utf16Buf[kd][1])
		d.graphemeBuf[kd] = append(d.graphemeBuf[kd], r)
	} else if utf16.IsSurrogate(r) {
		// This is the first half of a UTF-16 surrogate pair.
		d.utf16Half[kd] = true
		d.utf16Buf[kd][0] = r
	} else {
		// This should be a single UTF-16 that can be converted
		// to UTF-8.
		d.graphemeBuf[kd] = append(d.graphemeBuf[kd], r)
	}
}

// deserializeWin32Input deserializes the Win32 input events converting
// KeyEventRecrods to bytes. Before returning the bytes, it will also try to
// decode any UTF-16 pairs that might be present in the input buffer.
func (d *eventScanner) deserializeWin32Input(buf []byte) (int, []byte) {
	p := ansi.GetParser()
	defer ansi.PutParser(p)

	var processed int
	var state byte
	des := make([]byte, 0, len(buf))

	for len(buf) > 0 {
		seq, width, n, newState := ansi.DecodeSequence(buf, state, p)
		if n <= 0 {
			// A partial UTF-8/ANSI unit can make the stateless decoder wait.
			// Keep the bytes for the framed scanner instead of spinning here.
			des = append(des, d.encodeGraphemeBufs()...)
			des = append(des, buf...)
			break
		}
		switch width {
		case 0:
			if p.Command() == '_' && len(p.Params()) == 6 { // Win32 Input Mode
				vk, _ := p.Param(0, 0)
				if vk == 0 {
					// This is either a serialized KeyEventRecord or a UTF-16
					// pair.
					uc, _ := p.Param(2, 0)
					kd, _ := p.Param(3, 0)
					kd = clamp(kd, 0, 1) // kd is the key down state (0 or 1)
					d.storeGraphemeRune(kd, rune(uc))
					processed += n
					break
				}
			}
			fallthrough
		default:
			des = append(des, d.encodeGraphemeBufs()...)
			des = append(des, seq...)
		}

		state = newState
		buf = buf[n:]
	}

	des = append(des, d.encodeGraphemeBufs()...)

	return processed, des
}

func (d *TerminalReader) logf(format string, v ...interface{}) {
	logf(d.logger, format, v...)
}

func logf(logger Logger, format string, v ...interface{}) {
	if logger == nil {
		return
	}
	logger.Printf(format, v...)
}
