package computeruse

import "context"

func (p *Pane) operatorFrameInput(ctx context.Context, request PaneRequest) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || request.Sequence == 0 || request.Sequence != p.sequence.Load() {
		return ErrNotAllowed
	}
	local, err := p.session.operatorContext(ctx)
	if err != nil {
		return err
	}
	if request.FrameID == "" || request.Ref != "" {
		return ErrNotAllowed
	}
	switch request.Action {
	case "click_at":
		if request.X == nil || request.Y == nil || request.Text != "" {
			return ErrNotAllowed
		}
		return p.session.ClickFrame(local, request.FrameID, *request.X, *request.Y)
	case "type_text":
		if request.X != nil || request.Y != nil {
			return ErrNotAllowed
		}
		return p.session.TypeTextFrame(local, request.FrameID, request.Text)
	default:
		return ErrNotAllowed
	}
}
