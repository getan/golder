package computeruse

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

const (
	paneRequestLimit  = 8 << 10
	paneResponseLimit = 12 << 20
)

// PaneRequest is private to a Golder-owned, kernel-verified operator pane. It
// intentionally has no target selector, shell, arbitrary OS handles or files.
type PaneRequest struct {
	Action   string   `json:"action"`
	Ref      string   `json:"ref,omitempty"`
	Text     string   `json:"text,omitempty"`
	Sequence uint64   `json:"sequence,omitempty"`
	FrameID  string   `json:"frame_id,omitempty"`
	X        *float64 `json:"x,omitempty"`
	Y        *float64 `json:"y,omitempty"`
}

type PaneResponse struct {
	Status Status `json:"status"`
	Frame  *Frame `json:"frame,omitempty"`
	Text   string `json:"text,omitempty"`
	Error  string `json:"error,omitempty"`
	Busy   bool   `json:"busy,omitempty"`
}

// ServeOperatorPane serves one already authenticated connection. The caller
// MUST verify its exact pane identity with VerifyOperatorPanePeer before using
// this function and keep it private from model-facing tools. If several
// connections share a Pane, they must all be pinned to the same verified
// process; disconnect or cancellation pauses the session before returning.
func ServeOperatorPane(ctx context.Context, conn net.Conn, pane *Pane) error {
	if conn == nil || pane == nil || pane.session == nil {
		return ErrUnavailable
	}
	defer pane.Close()
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()
	reader := bufio.NewReader(conn)
	for {
		line, err := readPaneLine(reader, paneRequestLimit)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		var request PaneRequest
		if err := decodePaneJSON(line, &request); err != nil {
			return errors.New("invalid computer pane request")
		}
		response := paneResponse(ctx, pane, request)
		data, err := json.Marshal(response)
		if err != nil || len(data) > paneResponseLimit {
			return errors.New("computer pane response exceeds limit")
		}
		data = append(data, '\n')
		if err := writePaneBytes(conn, data); err != nil {
			return err
		}
		if request.Action == "stop" && response.Error == "" {
			return nil
		}
	}
}

func paneResponse(ctx context.Context, pane *Pane, request PaneRequest) PaneResponse {
	response := PaneResponse{}
	var err error
	if request.Action != "click" && request.Action != "fill" && request.Ref != "" {
		err = errors.New("ref is only valid for click or fill")
	} else if request.Action != "fill" && request.Action != "type_text" && request.Text != "" {
		err = errors.New("text is only valid for fill")
	} else if request.Action != "click_at" && request.Action != "type_text" && (request.FrameID != "" || request.X != nil || request.Y != nil) {
		err = errors.New("frame coordinates are only valid for local click_at or type_text")
	} else if request.Action != "status" && request.Action != "frame" && request.Action != "candidate" && !pane.acceptSequence(request.Sequence) {
		err = errors.New("computer pane command is stale or unsequenced")
	} else if (request.Action == "status" || request.Action == "frame" || request.Action == "candidate") && request.Sequence != 0 {
		err = errors.New("observation request cannot change the operator sequence")
	} else {
		switch request.Action {
		case "status":
		case "frame":
			var frame Frame
			if pane.session.Status().OperatorControl {
				var local context.Context
				local, err = pane.session.operatorContext(ctx)
				if err == nil {
					frame, err = pane.session.CapturePreview(local)
				}
			} else {
				frame, err = pane.session.CapturePreview(ctx)
			}
			if err == nil {
				response.Frame = &frame
			} else if errors.Is(err, ErrPreviewBusy) || errors.Is(err, context.Canceled) && ctx.Err() == nil {
				response.Busy, err = true, nil
			}
		case "candidate":
			var frame Frame
			frame, err = pane.session.operatorPreviewFrame()
			if err == nil {
				pane.mu.Lock()
				pane.previewDelivered = true
				pane.mu.Unlock()
				response.Frame = &frame
			}
		case "resume":
			resumeCtx, cancel := context.WithTimeout(ctx, OperatorResumeTimeout)
			response.Text, err = pane.operatorCommand(resumeCtx, request.Action, request.Sequence)
			cancel()
		case "observe", "pause", "takeover", "stop", "confirm-preview", "skip-preview":
			response.Text, err = pane.operatorCommand(ctx, request.Action, request.Sequence)
		case "click_at", "type_text":
			err = pane.operatorFrameInput(ctx, request)
			if err == nil {
				response.Text = "Input dispatched. Check the preview before the next action."
			}
		case "click":
			response.Text, err = pane.operatorCommand(ctx, "click "+request.Ref, request.Sequence)
		case "fill":
			response.Text, err = pane.operatorFill(ctx, request.Ref, request.Text, request.Sequence)
		default:
			err = errors.New("unknown computer pane action")
		}
	}
	response.Status = pane.session.Status()
	if err != nil {
		response.Error = err.Error()
	}
	return response
}

// PaneClient is used by the dedicated operator pane, never by the model. Its
// bounded protocol transfers frames only in memory and never writes them to a
// screenshot file or the conversation transcript.
type PaneClient struct {
	mu     sync.Mutex
	conn   net.Conn
	reader *bufio.Reader
}

func NewPaneClient(conn net.Conn) *PaneClient {
	return &PaneClient{conn: conn, reader: bufio.NewReader(conn)}
}

func (c *PaneClient) Request(ctx context.Context, request PaneRequest) (PaneResponse, error) {
	if c == nil || c.conn == nil {
		return PaneResponse{}, ErrUnavailable
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return PaneResponse{}, err
	}
	deadline := time.Now().Add(30 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := c.conn.SetDeadline(deadline); err != nil {
		return PaneResponse{}, err
	}
	complete := false
	defer func() {
		// A request may have taken effect even if its reply was lost. Do not
		// reuse the stream or accidentally consume that reply as another call.
		if !complete {
			_ = c.conn.Close()
		}
	}()
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = c.conn.SetDeadline(time.Now())
		close(interrupted)
	})
	defer func() {
		if !stop() {
			<-interrupted
		}
	}()
	data, err := json.Marshal(request)
	if err != nil || len(data) > paneRequestLimit {
		return PaneResponse{}, errors.New("invalid computer pane request")
	}
	if err := writePaneBytes(c.conn, append(data, '\n')); err != nil {
		return PaneResponse{}, err
	}
	line, err := readPaneLine(c.reader, paneResponseLimit)
	if err != nil {
		return PaneResponse{}, err
	}
	var response PaneResponse
	if err := decodePaneJSON(line, &response); err != nil {
		return PaneResponse{}, errors.New("invalid computer pane response")
	}
	if err := ctx.Err(); err != nil {
		return PaneResponse{}, err
	}
	complete = true
	return response, nil
}

func (c *PaneClient) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

func readPaneLine(reader *bufio.Reader, limit int) ([]byte, error) {
	var line []byte
	for {
		part, err := reader.ReadSlice('\n')
		if len(line)+len(part) > limit {
			return nil, fmt.Errorf("computer pane message exceeds %d bytes", limit)
		}
		line = append(line, part...)
		if err == nil {
			return line, nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			if errors.Is(err, io.EOF) && len(line) > 0 {
				return nil, io.ErrUnexpectedEOF
			}
			return nil, err
		}
	}
}

func decodePaneJSON(data []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing computer pane data")
	}
	return nil
}
