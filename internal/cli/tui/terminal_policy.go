package tui

import (
	"bytes"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/charmbracelet/x/ansi"

	"github.com/getan/golder/internal/diagtrace"
)

// Only automatic capability/color queries are suppressed. Explicit clipboard
// requests still work. Setting keyboard modes also remains intact, so terminals
// that support disambiguated keys retain Shift+Enter/IME behavior without a
// query/reply handshake. Local sessions retain their current negotiation.
func startupQueriesDisabled() bool {
	if isSSHSession() {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("GOLDER_NO_TERMINAL_QUERIES"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func prepareTerminalPalette(noQueries bool) {
	if noQueries {
		SetMarkdownDark(true)
	}
}

var automaticTerminalQueries = [][]byte{
	[]byte(ansi.RequestKittyKeyboard),
	[]byte(ansi.RequestBackgroundColor), []byte("\x1b]11;?\x1b\\"),
	[]byte(ansi.RequestForegroundColor), []byte("\x1b]10;?\x1b\\"),
	[]byte(ansi.RequestCursorColor), []byte("\x1b]12;?\x1b\\"),
	[]byte(ansi.RequestModeSynchronizedOutput), []byte(ansi.RequestModeUnicodeCore),
	[]byte(ansi.RequestPrimaryDeviceAttributes), []byte("\x1b[0c"),
}

// queryOutput is a narrow compatibility shim for Bubble Tea v2.0.8, whose
// renderer has no public option to omit its unconditional Kitty query. Keep
// this at the OUTPUT boundary rather than dropping replies or timing out input
// frames: no input data is inspected, discarded or read by a second goroutine.
// Remove it when Bubble Tea exposes an equivalent opt-out.
//
// A File is embedded to preserve tty identity/raw-mode/size/color detection.
// WriteString and ReadFrom are overridden so promoted file methods cannot
// bypass Write. Only proper prefixes of the fixed query list are buffered.
type queryOutput struct {
	*os.File
	sink    io.Writer
	mu      sync.Mutex
	pending []byte
}

func (w *queryOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	sourceLen := len(p)
	data := p
	if len(w.pending) > 0 {
		data = append(w.pending, p...)
		w.pending = nil
	}
	var out bytes.Buffer
	out.Grow(len(data))
	suppressed := 0
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\x1b')
		if i < 0 {
			out.Write(data)
			break
		}
		out.Write(data[:i])
		data = data[i:]
		matched, partial := 0, false
		for _, query := range automaticTerminalQueries {
			if bytes.HasPrefix(data, query) {
				matched = len(query)
				break
			}
			if bytes.HasPrefix(query, data) {
				partial = true
			}
		}
		if matched > 0 {
			suppressed++
			data = data[matched:]
			continue
		}
		if partial {
			w.pending = bytes.Clone(data)
			break
		}
		out.WriteByte(data[0])
		data = data[1:]
	}
	if suppressed > 0 {
		diagtrace.Event("tui", "automatic queries suppressed count=%d", suppressed)
	}
	if out.Len() > 0 {
		n, err := w.sink.Write(out.Bytes())
		if err != nil {
			return 0, err
		}
		if n != out.Len() {
			return 0, io.ErrShortWrite
		}
	}
	return sourceLen, nil
}

func (w *queryOutput) WriteString(s string) (int, error) { return w.Write([]byte(s)) }

func (w *queryOutput) ReadFrom(r io.Reader) (int64, error) {
	return io.Copy(struct{ io.Writer }{w}, r)
}

// Flush preserves any final incomplete output prefix during teardown. It does
// not close the borrowed tty. Ordinary complete frames leave no pending bytes.
func (w *queryOutput) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pending) == 0 {
		return nil
	}
	n, err := w.sink.Write(w.pending)
	if err == nil && n != len(w.pending) {
		err = io.ErrShortWrite
	}
	w.pending = nil
	return err
}
