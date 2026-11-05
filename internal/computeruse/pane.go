package computeruse

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"
)

// Pane is the state and rendering logic for a dedicated, operator-controlled
// terminal pane. The caller owns its PTY and must authenticate input as coming
// from the operator; this type must not be exposed as a model-callable socket.
type Pane struct {
	mu               sync.Mutex
	renderMu         sync.Mutex
	session          *Session
	renderer         *KittyRenderer
	out              io.Writer
	snapshot         Snapshot
	previewDelivered bool
	closed           bool
	sequence         atomic.Uint64
}

func NewPane(session *Session, out io.Writer, imageID uint32, graphicsSupported bool) (*Pane, error) {
	if session == nil {
		return nil, errors.New("computer pane requires a session")
	}
	renderer, err := NewKittyRenderer(out, imageID, graphicsSupported)
	if err != nil {
		return nil, err
	}
	return &Pane{session: session, renderer: renderer, out: out}, nil
}

// Render refreshes the target frame on a capable terminal; a non-graphics
// terminal receives status only. The caller positions the cursor and owns
// terminal resize handling.
func (p *Pane) Render(ctx context.Context, columns, rows int) error {
	if p == nil || p.session == nil {
		return ErrUnavailable
	}
	p.renderMu.Lock()
	defer p.renderMu.Unlock()
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return errors.New("computer pane is closed")
	}
	status := p.session.Status()
	label := "stopped"
	if status.Active && status.Paused {
		label = "paused"
	} else if status.Active && status.Capabilities.Control {
		label = "control allowed"
	} else if status.Active {
		label = "observe only"
	}
	line := fmt.Sprintf("Computer Use · %s · %s / %s\n", label, paneText(status.Target.Application), paneText(status.Target.Window))
	if err := writePaneBytes(p.out, []byte(line)); err != nil {
		return err
	}
	if !status.Active || status.Paused || !p.renderer.enabled {
		return nil
	}
	frame, err := p.session.Capture(ctx)
	if err != nil {
		return err
	}
	return p.renderer.Frame(frame, columns, rows)
}

// OperatorCommand handles only local, person-originated input. It never
// accepts a window identity or a model request, and resume is operator-only.
func (p *Pane) OperatorCommand(ctx context.Context, input string) (string, error) {
	return p.operatorCommand(ctx, input, 0)
}

func (p *Pane) operatorCommand(ctx context.Context, input string, sequence uint64) (string, error) {
	if p == nil || p.session == nil {
		return "", ErrUnavailable
	}
	fields := strings.Fields(input)
	// Revocation must interrupt an in-flight observation or action before
	// waiting for the command mutex. Invalid commands have no side effects.
	if len(fields) == 1 && fields[0] == "pause" {
		p.session.Pause()
	} else if len(fields) == 1 && fields[0] == "stop" {
		p.session.Stop()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return "", errors.New("computer pane is closed")
	}
	if sequence != 0 && sequence != p.sequence.Load() {
		return "", errors.New("computer pane command superseded by a newer operator command")
	}
	if len(fields) == 0 {
		return "Commands: observe, click <ref>, fill <ref> <text>, pause, resume, stop", nil
	}
	switch fields[0] {
	case "confirm-preview":
		if len(fields) != 1 || !p.previewDelivered {
			return "", ErrNotAllowed
		}
		if err := p.session.finishOperatorPreview(ctx, true); err != nil {
			return "", err
		}
		p.previewDelivered = false
		return "Window match confirmed. Computer Use may start.", nil
	case "skip-preview":
		if len(fields) != 1 {
			return "", ErrNotAllowed
		}
		if err := p.session.finishOperatorPreview(ctx, false); err != nil {
			return "", err
		}
		p.previewDelivered = false
		return "Preview declined. Only semantic access is available.", nil
	case "observe":
		if len(fields) != 1 {
			return "", errors.New("usage: observe")
		}
		snapshot, err := p.session.Observe(ctx)
		if err != nil {
			return "", err
		}
		p.snapshot = snapshot
		var text strings.Builder
		for _, element := range snapshot.Elements {
			fmt.Fprintf(&text, "%s  %s  %s\n", paneText(element.Ref), paneText(element.Role), paneText(element.Name))
		}
		if snapshot.Truncated {
			text.WriteString("[observation truncated]\n")
		}
		return text.String(), nil
	case "click":
		if len(fields) != 2 {
			return "", errors.New("usage: click <ref>")
		}
		id := p.snapshot.ID
		p.snapshot = Snapshot{}
		if err := p.session.Act(ctx, id, fields[1], "click", ""); err != nil {
			return "", err
		}
		return "Clicked. Observe again before the next action.", nil
	case "pause":
		if len(fields) != 1 {
			return "", errors.New("usage: pause")
		}
		p.snapshot = Snapshot{}
		p.session.Pause()
		return "Paused. The operator may take over the target window.", nil
	case "resume":
		if len(fields) != 1 {
			return "", errors.New("usage: resume")
		}
		if err := p.session.Resume(ctx); err != nil {
			return "", err
		}
		return "Resumed by the operator.", nil
	case "stop":
		if len(fields) != 1 {
			return "", errors.New("usage: stop")
		}
		p.snapshot = Snapshot{}
		p.session.Stop()
		return "Stopped.", nil
	default:
		return "", errors.New("unknown computer pane command")
	}
}

func (p *Pane) operatorFill(ctx context.Context, ref, text string, sequence uint64) (string, error) {
	if p == nil || p.session == nil {
		return "", ErrUnavailable
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return "", errors.New("computer pane is closed")
	}
	if sequence == 0 || sequence != p.sequence.Load() {
		return "", errors.New("computer pane command superseded by a newer operator command")
	}
	id := p.snapshot.ID
	p.snapshot = Snapshot{}
	if err := p.session.Act(ctx, id, ref, "fill", text); err != nil {
		return "", err
	}
	return "Text set. Observe again before the next action.", nil
}

func (p *Pane) acceptSequence(sequence uint64) bool {
	if sequence == 0 {
		return false
	}
	for {
		previous := p.sequence.Load()
		if sequence <= previous {
			return false
		}
		if p.sequence.CompareAndSwap(previous, sequence) {
			return true
		}
	}
}

// Close removes the frame and pauses control if the pane goes away. Closing a
// view must never leave an unseen desktop control session running.
func (p *Pane) Close() error {
	if p == nil {
		return nil
	}
	p.session.Pause()
	p.mu.Lock()
	p.closed = true
	p.snapshot = Snapshot{}
	p.session.Pause()
	p.mu.Unlock()
	p.renderMu.Lock()
	defer p.renderMu.Unlock()
	return p.renderer.Close()
}

func paneText(text string) string {
	var result strings.Builder
	for _, r := range text {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		if result.Len() >= 256 {
			break
		}
		result.WriteRune(r)
	}
	return result.String()
}
