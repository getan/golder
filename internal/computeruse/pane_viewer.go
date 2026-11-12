package computeruse

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/term"
	"github.com/getan/golder/internal/tuiosctl"
)

type paneViewerEvent struct {
	action   string
	sequence uint64
	response PaneResponse
	err      error
}

type paneViewerState struct {
	status      Status
	frame       *Frame
	message     string
	input       string
	semantic    bool
	localPaused bool
	scroll      int
	graphics    bool
}

// RunOperatorPane is the internal child entry point for the dedicated tuios
// view. The parent authenticates all three socket peers before serving them.
// It must not be used as a model-facing desktop control interface.
func RunOperatorPane(ctx context.Context, socket string, in, out *os.File) error {
	if !tuiosctl.Available() || !filepath.IsAbs(socket) || in == nil || out == nil || !term.IsTerminal(in.Fd()) || !term.IsTerminal(out.Fd()) {
		return errors.New("computer pane requires its own local tuios terminal")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	clients := make([]*PaneClient, 0, 3)
	defer func() {
		for _, client := range clients {
			_ = client.Close()
		}
	}()
	for range 3 {
		conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socket)
		if err != nil {
			return fmt.Errorf("computer pane connection: %w", err)
		}
		clients = append(clients, NewPaneClient(conn))
	}
	old, err := term.MakeRaw(in.Fd())
	if err != nil {
		return err
	}
	defer term.Restore(in.Fd(), old)
	graphics := queryPaneGraphics(in, out)
	if err := writePaneBytes(out, []byte("\x1b[?1049h\x1b[?25l\x1b[?7l\x1b[?1000h\x1b[?1006h")); err != nil {
		return err
	}
	defer func() { _ = writePaneBytes(out, []byte("\x1b[?1006l\x1b[?1000l\x1b[?7h\x1b[?25h\x1b[?1049l")) }()
	renderer, err := NewKittyRenderer(out, 0x474f4c44, graphics)
	if err != nil {
		return err
	}
	defer renderer.Close()

	updates := make(chan paneViewerEvent, 8)
	keys := make(chan rune, 64)
	clicks := make(chan paneMouseClick, 8)
	var captureAllowed atomic.Bool
	var previewPending atomic.Bool
	go func() {
		reader := bufio.NewReader(in)
		var filter paneInputFilter
		for {
			b, size, err := reader.ReadRune()
			if err != nil {
				select {
				case updates <- paneViewerEvent{err: err}:
				case <-ctx.Done():
				}
				return
			}
			if b == utf8.RuneError && size == 1 {
				select {
				case updates <- paneViewerEvent{err: errors.New("invalid terminal UTF-8 input")}:
				case <-ctx.Done():
				}
				return
			}
			b, ok := filter.accept(b)
			if filter.mouse != nil {
				select {
				case clicks <- *filter.mouse:
				case <-ctx.Done():
					return
				}
			}
			if !ok {
				continue
			}
			select {
			case keys <- b:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			action := "status"
			if previewPending.Load() && graphics {
				action = "candidate"
			} else if captureAllowed.Load() {
				action = "frame"
			}
			response, err := clients[0].Request(ctx, PaneRequest{Action: action})
			select {
			case updates <- paneViewerEvent{action: action, response: response, err: err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
			select {
			case <-ticker.C:
			case <-ctx.Done():
				return
			}
		}
	}()
	state := paneViewerState{graphics: graphics, message: "Agent 控制 / Agent control · Ctrl+G 人工接管 / take over"}
	if !graphics {
		state.message = "Kitty graphics unavailable; type observe for semantic controls. Ctrl+G pauses."
	}
	var display operatorPaneDisplay
	if err := display.drawTerminal(out, renderer, state); err != nil {
		return err
	}
	statusKnown, previewSettled, previewRendered := false, false, false
	var sequence atomic.Uint64
	requestFull := func(req PaneRequest, emergency bool) {
		client := clients[1]
		if emergency {
			client = clients[2]
		}
		id := sequence.Add(1)
		req.Sequence = id
		go func() {
			response, err := client.Request(ctx, req)
			select {
			case updates <- paneViewerEvent{action: req.Action, sequence: id, response: response, err: err}:
			case <-ctx.Done():
			}
		}()
	}
	request := func(action, ref, text string, emergency bool) {
		requestFull(PaneRequest{Action: action, Ref: ref, Text: text}, emergency)
	}
	localInputPending := false
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event := <-updates:
			if event.err != nil {
				return fmt.Errorf("computer pane disconnected: %w", event.err)
			}
			if event.sequence != 0 && event.sequence != sequence.Load() {
				continue
			}
			if previewSettled && event.response.Status.PendingPreview {
				continue
			}
			if event.action == "takeover" || event.action == "pause" || event.action == "resume" {
				localInputPending = false
			}
			state.status = event.response.Status
			if !state.status.Active {
				return nil
			}
			wasPending := previewPending.Swap(state.status.PendingPreview)
			if state.status.PendingPreview {
				captureAllowed.Store(false)
				if !graphics {
					state.message = "Preview requires Kitty graphics. Type skip-preview for semantic-only access, or stop."
				}
			} else if !statusKnown || wasPending {
				captureAllowed.Store(graphics && !state.localPaused)
				if wasPending {
					state.frame = nil
				}
			}
			statusKnown = true
			if state.status.OperatorControl {
				state.localPaused = false
				captureAllowed.Store(graphics && !localInputPending)
			} else if state.status.Paused {
				state.localPaused, state.frame = true, nil
				captureAllowed.Store(false)
			}
			if event.action == "resume" && event.response.Error == "" && !state.status.Paused {
				state.localPaused = false
				captureAllowed.Store(graphics)
			}
			if event.action == "frame" && event.response.Error != "" {
				state.frame = nil
				if !state.status.Paused || state.status.OperatorControl {
					state.message = "Preview temporarily unavailable; retrying: " + event.response.Error
				}
			}
			if event.action == "frame" && !state.localPaused && !localInputPending && event.response.Frame != nil {
				state.frame = event.response.Frame
			}
			if event.action == "click_at" || event.action == "type_text" {
				localInputPending = false
				captureAllowed.Store(graphics && state.status.OperatorControl)
			}
			if event.action == "candidate" && state.status.PendingPreview && event.response.Frame != nil {
				state.frame, state.semantic = event.response.Frame, false
				state.message = "Confirm this preview matches the selected target: type confirm-preview, or stop."
			}
			if event.action != "frame" && event.action != "status" && event.action != "candidate" {
				state.message = event.response.Text
				if event.response.Error != "" {
					state.message = "Error: " + event.response.Error
				}
				if (event.action == "confirm-preview" || event.action == "skip-preview") && event.response.Error == "" {
					previewSettled = true
				}
				if event.action == "observe" && event.response.Error == "" {
					state.semantic, state.scroll = true, 0
				}
			}
		case click := <-clicks:
			width, height, err := term.GetSize(out.Fd())
			if err != nil || !display.drawn || display.last.width != width || display.last.height != height {
				state.message = "Pane resized; wait for the updated preview and click again."
				break
			}
			x, y, ok := panePreviewPoint(state, click, width, height)
			if !ok || localInputPending {
				state.message = "Ctrl+G 人工接管 / take over before clicking the preview."
				break
			}
			requestFull(PaneRequest{Action: "click_at", FrameID: state.frame.ID, X: &x, Y: &y}, false)
			localInputPending, state.frame = true, nil
			captureAllowed.Store(false)
		case b := <-keys:
			switch b {
			case 0x07: // Ctrl+G is independent of a slow observation or frame.
				state.localPaused, state.frame = true, nil
				localInputPending = false
				state.input = ""
				captureAllowed.Store(false)
				state.message = "人工接管中 / Taking over; Agent input is being canceled."
				request("takeover", "", "", true)
			case 0x03: // Closing the connections also stops the parent session.
				return nil
			case '\r', '\n':
				line := state.input
				fields := strings.Fields(line)
				state.input = ""
				if len(fields) == 0 {
					break
				}
				switch fields[0] {
				case "type":
					value := strings.TrimPrefix(line, "type ")
					if !state.status.OperatorControl || state.frame == nil || localInputPending || value == line || !validFrameText(value) {
						state.message = "Ctrl+G take over, click a field, then type <text> and Enter."
						break
					}
					requestFull(PaneRequest{Action: "type_text", FrameID: state.frame.ID, Text: value}, false)
					localInputPending, state.frame = true, nil
					captureAllowed.Store(false)
				case "takeover":
					request("takeover", "", "", true)
				case "confirm-preview":
					if len(fields) == 1 && previewRendered && state.status.PendingPreview && state.frame != nil {
						request("confirm-preview", "", "", false)
					} else {
						state.message = "A visible local preview is required before confirming."
					}
				case "skip-preview":
					if len(fields) == 1 && state.status.PendingPreview {
						request("skip-preview", "", "", false)
					}
				case "pause", "p":
					state.localPaused, state.frame = true, nil
					captureAllowed.Store(false)
					request("pause", "", "", true)
				case "stop", "q":
					return nil
				case "resume", "r":
					request("resume", "", "", false)
				case "observe", "o":
					request("observe", "", "", false)
				case "click", "c":
					if len(fields) == 2 {
						request("click", fields[1], "", false)
					} else {
						state.message = "Usage: click @e1 (observe first)"
					}
				case "fill", "f":
					ref, value, ok := paneFillArguments(line)
					if ok {
						request("fill", ref, value, false)
					} else {
						state.message = "Usage: fill @e1 text (observe first)"
					}
				case "view", "v":
					state.semantic = false
					if !state.localPaused {
						captureAllowed.Store(graphics)
					}
				case "down", "j":
					state.scroll++
				case "up", "k":
					state.scroll = max(0, state.scroll-1)
				default:
					state.message = "Commands: observe, click @e1, fill @e1 text, pause, resume, stop, view, up, down"
				}
			case 0x7f, 0x08:
				if len(state.input) > 0 {
					_, size := utf8.DecodeLastRuneInString(state.input)
					state.input = state.input[:len(state.input)-size]
				}
				if err := drawPanePrompt(out, state.input); err != nil {
					return err
				}
				continue
			default:
				if unicode.IsPrint(b) && len(state.input)+utf8.RuneLen(b) <= 4096 {
					state.input += string(b)
					if err := drawPanePrompt(out, state.input); err != nil {
						return err
					}
				}
				continue
			}
		}
		if err := display.drawTerminal(out, renderer, state); err != nil {
			return err
		}
		width, height, sizeErr := term.GetSize(out.Fd())
		previewRendered = sizeErr == nil && width > 0 && height > 4 && state.graphics && state.status.PendingPreview && !state.semantic && !state.localPaused && state.frame != nil
	}
}

type operatorPaneKey struct {
	status                Status
	message, input        string
	semantic, localPaused bool
	scroll                int
	graphics, hasFrame    bool
	frameHash             [sha256.Size]byte
	width, height         int
}

type operatorPaneDisplay struct {
	last  operatorPaneKey
	drawn bool
}

func (d *operatorPaneDisplay) drawTerminal(out *os.File, renderer *KittyRenderer, state paneViewerState) error {
	width, height, err := term.GetSize(out.Fd())
	if err != nil || width < 1 || height < 1 {
		width, height = 80, 24
	}
	width, height = min(width, 1000), min(height, 1000)
	return d.draw(out, renderer, state, width, height)
}

// Repeated captures have fresh frame IDs even when their pixels do not change.
// Avoid clearing and retransmitting the image on every status/preview tick.
func (d *operatorPaneDisplay) draw(out io.Writer, renderer *KittyRenderer, state paneViewerState, width, height int) (err error) {
	key := operatorPaneKey{status: state.status, message: state.message, input: state.input,
		semantic: state.semantic, localPaused: state.localPaused, scroll: state.scroll,
		graphics: state.graphics, width: width, height: height}
	key.hasFrame = !state.semantic && !state.localPaused && state.frame != nil && state.graphics && height > 4
	if key.hasFrame {
		key.frameHash = sha256.Sum256(state.frame.PNG)
	}
	if d.drawn && d.last == key {
		return nil
	}
	// Compatible terminals present the clear, text and image together instead
	// of showing the empty pane between them. Always release synchronized mode
	// even if image transmission fails.
	if err := writePaneBytes(out, []byte("\x1b[?2026h")); err != nil {
		return err
	}
	defer func() {
		endErr := writePaneBytes(out, []byte("\x1b[?2026l"))
		if err == nil {
			err = endErr
		}
		if err == nil {
			d.last, d.drawn = key, true
		} else {
			d.drawn = false
		}
	}()
	if err := renderer.Clear(); err != nil {
		return err
	}
	status := "connecting"
	if state.status.Active && state.status.OperatorControl {
		status = "人工控制 / Human control · Agent paused"
	} else if state.status.Active && state.localPaused {
		status = "PAUSED · operator takeover"
	} else if state.status.Active && state.status.Capabilities.Control {
		status = "Agent 控制 / Agent control"
	} else if state.status.Active {
		status = "observe only"
	}
	var text strings.Builder
	text.WriteString("\x1b[2J\x1b[H")
	fmt.Fprintf(&text, "%s\r\n", paneClip(fmt.Sprintf("Computer Use · %s · %s / %s", status, paneText(state.status.Target.Application), paneText(state.status.Target.Window)), width))
	if state.semantic {
		lines := strings.Split(state.message, "\n")
		start := min(state.scroll, len(lines))
		for _, line := range lines[start:min(len(lines), start+max(0, height-4))] {
			fmt.Fprintf(&text, "%s\r\n", paneClip(paneText(line), width))
		}
	} else if state.frame == nil {
		fmt.Fprintf(&text, "%s\r\n", paneClip(paneText(state.message), width))
	}
	if err := writePaneBytes(out, []byte(text.String())); err != nil {
		return err
	}
	if !state.semantic && !state.localPaused && state.frame != nil && state.graphics && height > 4 {
		if err := renderer.Frame(*state.frame, width, height-4); err != nil {
			return err
		}
	}
	help := "Ctrl+G 人工接管 / take over · observe · resume · stop · view"
	if state.status.OperatorControl {
		help = "Click preview · type <text> + Enter · resume → Agent · stop"
	}
	if state.status.PendingPreview {
		help = "confirm-preview · skip-preview · stop"
	}
	footer := fmt.Sprintf("\x1b[%d;1H%s\x1b[%d;1H%s\x1b[%d;1H%s", max(1, height-2), paneClip(state.message, width), max(1, height-1), paneClip(help, width), height, paneClip("> "+paneInputDisplay(state.input), width))
	return writePaneBytes(out, []byte(footer))
}

func drawPanePrompt(out *os.File, input string) error {
	width, height, err := term.GetSize(out.Fd())
	if err != nil || width < 1 || height < 1 {
		width, height = 80, 24
	}
	line := fmt.Sprintf("\x1b[%d;1H\x1b[2K%s", min(height, 1000), paneClip("> "+paneInputDisplay(input), min(width, 1000)))
	return writePaneBytes(out, []byte(line))
}

func paneFillArguments(line string) (string, string, bool) {
	line = strings.TrimLeftFunc(line, unicode.IsSpace)
	first := strings.IndexFunc(line, unicode.IsSpace)
	if first < 0 || (line[:first] != "fill" && line[:first] != "f") {
		return "", "", false
	}
	line = strings.TrimLeftFunc(line[first:], unicode.IsSpace)
	second := strings.IndexFunc(line, unicode.IsSpace)
	if second < 1 {
		return "", "", false
	}
	ref, text := line[:second], strings.TrimLeftFunc(line[second:], unicode.IsSpace)
	return ref, text, text != ""
}

// A typed value is never echoed into pane history; only its existence is shown.
func paneInputDisplay(line string) string {
	if strings.HasPrefix(line, "type ") {
		return "type •••"
	}
	ref, _, ok := paneFillArguments(line)
	if !ok {
		return line
	}
	return "fill " + ref + " •••"
}

func paneClip(text string, width int) string {
	runes := []rune(paneText(text))
	if len(runes) > max(0, width-1) {
		runes = runes[:max(0, width-1)]
	}
	return string(runes)
}
