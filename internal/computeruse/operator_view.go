package computeruse

import "context"

// OperatorView is a local-frontend facade, not a model tool. GUI/pane code owns
// it; no model-facing action can mint its takeover context or resume a session.
// It uses the same target, operation lock and handoff generation as the Agent.
type OperatorView struct{ session *Session }

func NewOperatorView(session *Session) *OperatorView { return &OperatorView{session: session} }

func (v *OperatorView) TakeOver() error {
	if v == nil || v.session == nil {
		return ErrUnavailable
	}
	return v.session.takeOver()
}

func (v *OperatorView) CapturePreview(ctx context.Context) (Frame, error) {
	if v == nil || v.session == nil {
		return Frame{}, ErrUnavailable
	}
	if v.session.Status().OperatorControl {
		local, err := v.session.operatorContext(ctx)
		if err != nil {
			return Frame{}, err
		}
		return v.session.CapturePreview(local)
	}
	return v.session.CapturePreview(ctx)
}

func (v *OperatorView) ClickFrame(ctx context.Context, frameID string, x, y float64) error {
	if v == nil || v.session == nil {
		return ErrUnavailable
	}
	local, err := v.session.operatorContext(ctx)
	if err != nil {
		return err
	}
	return v.session.ClickFrame(local, frameID, x, y)
}

func (v *OperatorView) TypeTextFrame(ctx context.Context, frameID, text string) error {
	if v == nil || v.session == nil {
		return ErrUnavailable
	}
	local, err := v.session.operatorContext(ctx)
	if err != nil {
		return err
	}
	return v.session.TypeTextFrame(local, frameID, text)
}
