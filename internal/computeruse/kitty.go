package computeruse

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	_ "image/png"
	"io"
	"sync"
)

// KittyRenderer draws ephemeral computer-use frames in a terminal pane that
// has already positively detected Kitty graphics support. It does not own the
// terminal, select targets, grant control, or forward input to a desktop.
type KittyRenderer struct {
	mu      sync.Mutex
	out     io.Writer
	imageID uint32
	enabled bool
	closed  bool
}

func NewKittyRenderer(out io.Writer, imageID uint32, graphicsSupported bool) (*KittyRenderer, error) {
	if out == nil || imageID == 0 {
		return nil, errors.New("computer pane requires an output and nonzero image id")
	}
	return &KittyRenderer{out: out, imageID: imageID, enabled: graphicsSupported}, nil
}

// Frame transmits a single PNG by direct, chunked data transfer. It does not
// expose a filesystem path to the terminal or persist the captured image.
func (r *KittyRenderer) Frame(frame Frame, columns, rows int) error {
	if r == nil {
		return errors.New("computer pane renderer unavailable")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errors.New("computer pane renderer is closed")
	}
	if !r.enabled {
		return nil
	}
	if columns < 1 || rows < 1 || columns > 1000 || rows > 1000 || len(frame.PNG) == 0 || len(frame.PNG) > MaxFrameBytes {
		return errors.New("invalid computer pane frame or geometry")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(frame.PNG))
	if err != nil || format != "png" || cfg.Width < 1 || cfg.Height < 1 || int64(cfg.Width)*int64(cfg.Height) > MaxFramePixels {
		return errors.New("invalid computer pane PNG")
	}
	encoded := base64.StdEncoding.EncodeToString(frame.PNG)
	first := true
	for len(encoded) > 0 {
		n := min(len(encoded), 4096)
		chunk := encoded[:n]
		encoded = encoded[n:]
		more := 0
		if len(encoded) > 0 {
			more = 1
		}
		prefix := fmt.Sprintf("\x1b_Gm=%d;", more)
		if first {
			prefix = fmt.Sprintf("\x1b_Ga=T,f=100,t=d,i=%d,c=%d,r=%d,C=1,q=2,m=%d;", r.imageID, columns, rows, more)
			first = false
		}
		if err := writePaneBytes(r.out, []byte(prefix+chunk+"\x1b\\")); err != nil {
			return err
		}
	}
	return nil
}

// Clear removes the current frame without closing the renderer, so a paused
// session never leaves its last captured image displayed in the pane.
func (r *KittyRenderer) Clear() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || !r.enabled {
		return nil
	}
	return r.clearLocked()
}

// Close removes only this pane's image. It is safe to call repeatedly.
func (r *KittyRenderer) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	if !r.enabled {
		return nil
	}
	return r.clearLocked()
}

func (r *KittyRenderer) clearLocked() error {
	return writePaneBytes(r.out, []byte(fmt.Sprintf("\x1b_Ga=d,d=I,i=%d,q=2\x1b\\", r.imageID)))
}

func writePaneBytes(out io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := out.Write(data)
		if err != nil {
			return err
		}
		if n <= 0 || n > len(data) {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
