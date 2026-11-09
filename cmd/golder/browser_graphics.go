package main

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

// Hosted panes have no local daemon socket, but their output still goes through
// a tuios renderer on the owning host. No control capability is implied here.
func browserPaneOutput(out io.Writer, getenv func(string) string) io.Writer {
	if out != nil && (strings.TrimSpace(getenv("TUIOS_ENV")) == "1" || strings.TrimSpace(getenv("TUIOS_PANE_HOSTED")) == "1") {
		return &browserFrameWriter{out: out}
	}
	return out
}

// browserFrameWriter adapts the pinned kitty-browse encoder for tuios-mini.
// Its a=T frames replace the tracked placement with a hidden record before the
// old host placement is removed. A workspace switch between the transmit and
// the next refresh can then leave the old image over another workspace.
//
// Transmit and place separately in a synchronized update: the existing
// placement stays tracked until the new one is ready, so a tab switch can hide
// it even while a large image is still arriving. Tuios translates the guest's
// image and placement ids per pane; this never deletes another pane's images.
// Only the complete, fixed-id frames emitted by our pinned browser are changed.
type browserFrameWriter struct {
	mu  sync.Mutex
	out io.Writer
	buf []byte
}

const browserSyncBegin = "\x1b[?2026h"
const browserSyncEnd = "\x1b[?2026l"

func (w *browserFrameWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	cols, rows, ok := browserKittyFrame(p)
	if !ok {
		return w.out.Write(p)
	}
	w.buf = append(w.buf[:0], browserSyncBegin...)
	w.buf = append(w.buf, p...)
	// The leading packet is "ESC _ G a = T". Use transmit-only, then issue
	// a separate placement after all of its continuation packets.
	w.buf[len(browserSyncBegin)+5] = 't'
	w.buf = fmt.Appendf(w.buf, "\x1b_Ga=p,i=1,p=1,c=%d,r=%d,C=1,q=2\x1b\\", cols, rows)
	w.buf = append(w.buf, browserSyncEnd...)
	length := len(w.buf)
	n, err := w.out.Write(w.buf)
	if err == nil && n != length {
		err = io.ErrShortWrite
	}
	if cap(w.buf) > 4<<20 {
		w.buf = nil
	} else {
		w.buf = w.buf[:0]
	}
	if err != nil {
		return max(0, min(len(p), n-len(browserSyncBegin))), err
	}
	return len(p), nil
}

// browserKittyFrame recognizes only a whole kitty-browse v0.2.11 frame; it
// deliberately leaves probes, screenshots, other image ids and malformed or
// partial writes untouched. The encoder writes all chunks in one Write.
func browserKittyFrame(p []byte) (cols, rows int, ok bool) {
	if !bytes.HasPrefix(p, []byte("\x1b_Ga=T,f=")) || !bytes.HasSuffix(p, []byte("\x1b\\")) {
		return 0, 0, false
	}
	end := bytes.IndexByte(p, ';')
	if end < 0 || end > 256 {
		return 0, 0, false
	}
	var image, placement, cursor, quiet, more string
	for _, field := range bytes.Split(p[3:end], []byte{','}) {
		key, value, found := bytes.Cut(field, []byte{'='})
		if !found {
			continue
		}
		switch string(key) {
		case "i":
			image = string(value)
		case "p":
			placement = string(value)
		case "C":
			cursor = string(value)
		case "q":
			quiet = string(value)
		case "m":
			more = string(value)
		case "c":
			cols, _ = strconv.Atoi(string(value))
		case "r":
			rows, _ = strconv.Atoi(string(value))
		}
	}
	if image != "1" || placement != "1" || cursor != "1" || quiet != "2" || cols <= 0 || rows <= 0 || cols > 65535 || rows > 65535 {
		return 0, 0, false
	}
	if more != "0" && (more != "1" || !bytes.Contains(p[end+1:], []byte("\x1b_Gm=0;"))) {
		return 0, 0, false
	}
	return cols, rows, true
}
