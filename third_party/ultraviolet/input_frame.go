package uv

import (
	"bytes"
	"time"
	"unicode/utf8"
)

// A lone ESC still uses EscTimeout. An introducer such as ESC [ also names an
// Alt key, so give it a separate grace period. Once a payload byte has arrived,
// a protocol frame is unambiguous and must not expire into ordinary key text.
const inputPrefixTimeout = time.Second

const (
	maxInputSequence = 4096
	maxInputString   = 4 << 20 // includes OSC 52 clipboard replies
	maxInputPaste    = 16 << 20
)

type inputFrameKind byte

const (
	inputPlain inputFrameKind = iota
	inputCSI
	inputSS3
	inputOSC
	inputString
)

type inputFrame struct {
	kind     inputFrameKind
	end      int
	complete bool
	cancel   bool // discard the prefix, but decode the interrupt/new ESC itself
}

func (f inputFrame) limit() int {
	if f.kind == inputOSC || f.kind == inputString {
		return maxInputString
	}
	return maxInputSequence
}

// frameInput recognizes boundaries, not event meaning. EventDecoder remains the
// only decoder. Holding a frame here also protects recognized events such as
// X10 mouse that the stateless decoder otherwise accepts before their payload.
func frameInput(b []byte) inputFrame {
	if len(b) == 0 {
		return inputFrame{}
	}
	off, intro := 1, b[0]
	if intro == 0x1b {
		if len(b) == 1 {
			return inputFrame{}
		}
		off, intro = 2, b[1]
		switch intro {
		case '[':
			intro = 0x9b
		case 'O':
			intro = 0x8f
		case ']':
			intro = 0x9d
		case 'P', '_', '^', 'X':
			intro = 0x90
		default:
			return inputFrame{}
		}
	}
	f := inputFrame{end: len(b)}
	switch intro {
	case 0x9b:
		f.kind = inputCSI
	case 0x8f:
		f.kind = inputSS3
	case 0x9d:
		f.kind = inputOSC
	case 0x90, 0x9f, 0x9e, 0x98:
		f.kind = inputString
	default:
		return inputFrame{}
	}
	for i := off; i < len(b); i++ {
		c := b[i]
		if c == 0x1b {
			if f.kind == inputOSC || f.kind == inputString {
				if i+1 == len(b) {
					return f // split ESC \\ terminator
				}
				if b[i+1] == '\\' {
					f.end, f.complete = i+2, true
					return f
				}
				if i == off {
					f.end, f.complete = i, true // Alt+] etc. followed by another key
					return f
				}
				if b[i+1] == 0x1b {
					f.end, f.cancel = i+1, true // cancelling ESC, then a new ESC
					return f
				}
			}
			f.end, f.cancel = i, true
			return f
		}
		// Real input interrupts a broken report. Do not swallow Ctrl+C, Ctrl+D,
		// Enter, or a fresh escape sequence while recovering from one.
		if c == 0x03 || c == 0x04 || c == '\r' || c == '\n' || c == 0x18 || c == 0x1a {
			f.end, f.cancel = i, true
			if c == 0x18 || c == 0x1a {
				f.end++ // CAN/SUB cancel the report; they are not keyboard events
			}
			return f
		}
		if f.kind == inputOSC || f.kind == inputString {
			if c == 0x9c || c == 0x07 && f.kind == inputOSC {
				f.end, f.complete = i+1, true
				return f
			}
			if c >= utf8.RuneSelf && utf8.RuneStart(c) {
				_, size := utf8.DecodeRune(b[i:])
				i += size - 1 // a UTF-8 continuation byte is not an 8-bit ST
			}
			continue
		}
		if isUTF8TextLead(c) || c == 0x7f {
			// A CSI/SS3 frame is ASCII: parameters (0x30-0x3f), intermediates
			// (0x20-0x2f) and one final byte (0x40-0x7e). A valid UTF-8 lead
			// byte (CJK, emoji, ...) can only be typed text, so the frame ends
			// before it and those bytes decode as input. Treating them as
			// payload swallowed every CJK character typed after a split "ESC [":
			// no UTF-8 byte is a CSI final byte, and no idle timer runs while a
			// frame is pending. Invalid bytes such as 0xff stay in the malformed
			// frame so their tail is never typed. DEL cannot appear in a frame.
			// X10's three raw payload bytes are consumed above, before this.
			f.end, f.cancel = i, true
			return f
		}
		if c >= 0x40 && c <= 0x7e {
			f.end, f.complete = i+1, true
			// X10: CSI M is followed by three raw bytes, not three keys.
			if f.kind == inputCSI && i == off && c == 'M' {
				f.end = i + 4
				f.complete = len(b) >= f.end
			}
			return f
		}
	}
	return f
}

// discardInput consumes an oversized report without ever exposing its tail as
// keys. Keep only a trailing ESC so a split ST is still recognized. Recovery
// at a new ESC/control key remains possible even if no terminator ever comes.
func discardInput(b []byte, kind inputFrameKind) (n int, done bool) {
	for i, c := range b {
		if c == 0x1b {
			if kind == inputOSC || kind == inputString {
				if i+1 == len(b) {
					return i, false
				}
				if b[i+1] == '\\' {
					return i + 2, true
				}
			}
			return i, true
		}
		if c == 0x03 || c == 0x04 || c == '\r' || c == '\n' || c == 0x18 || c == 0x1a {
			return i, true
		}
		if kind == inputCSI || kind == inputSS3 {
			if c >= 0x40 && c <= 0x7e {
				return i + 1, true
			}
			// Recovery must not swallow typed text: a valid UTF-8 lead byte or
			// DEL cannot belong to an ASCII frame, so return to ordinary
			// decoding. Invalid bytes remain part of the discarded frame.
			if isUTF8TextLead(c) || c == 0x7f {
				return i, true
			}
		} else if c == 0x9c || c == 0x07 && kind == inputOSC {
			return i + 1, true
		}
	}
	return len(b), false
}

// isUTF8TextLead reports whether b starts a well-formed multi-byte UTF-8
// sequence, excluding the continuation bytes (0x80-0xbf), the invalid 0xC0/0xC1
// and the out-of-range 0xF5-0xff. Those belong to a malformed escape frame and
// must never be released as typing.
func isUTF8TextLead(b byte) bool { return b >= 0xC2 && b <= 0xF4 }

var pasteEndMarkers = [][]byte{[]byte("\x1b[201~"), []byte("\x9b201~")}

// consumePaste treats the payload as bytes, never as terminal events. Mouse-
// looking text and control characters inside a bracketed paste are user data.
// Leave a possible partial end marker in the caller's input buffer.
func (d *eventScanner) consumePaste(b []byte) (n int, ended bool) {
	end, markerLen := len(b), 0
	for _, marker := range pasteEndMarkers {
		if i := bytes.Index(b, marker); i >= 0 && (markerLen == 0 || i < end) {
			end, markerLen = i, len(marker)
		}
	}
	if markerLen == 0 {
		for _, marker := range pasteEndMarkers {
			for size := 1; size < len(marker) && size <= len(b); size++ {
				if bytes.HasSuffix(b, marker[:size]) && len(b)-size < end {
					end = len(b) - size
				}
			}
		}
	}
	if len(d.paste)+end <= maxInputPaste && !d.pasteOverflow {
		d.paste = append(d.paste, b[:end]...)
	} else {
		d.paste, d.pasteOverflow = d.paste[:0], true
	}
	return end + markerLen, markerLen != 0
}

func (d *TerminalReader) inputWait(b []byte) time.Duration {
	if len(b) == 0 || d.eventScanner.discard != inputPlain || d.eventScanner.paste != nil {
		return 0
	}
	f := frameInput(b)
	if f.kind != inputPlain && !f.complete && !f.cancel {
		if len(b) == 2 && b[0] == 0x1b {
			return inputPrefixTimeout
		}
		return 0 // continuation, not an ambiguous Esc key; no idle tick chain
	}
	if !utf8.FullRune(b) {
		return 0
	}
	return d.EscTimeout
}
