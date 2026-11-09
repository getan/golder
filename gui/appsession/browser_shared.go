package appsession

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/getan/kitty-browse/browserhost"

	"github.com/getan/golder/internal/browserctl"
)

// RegisterSharedBrowser retains the operator capability only in trusted GUI
// memory. Neither the automation socket nor transcript carries this authority.
func RegisterSharedBrowser(view *browserhost.RemoteView) func() {
	stop := view.KeepAlive(context.Background())
	unregister := RegisterBrowserOperator(view.Socket, sharedBrowserOperator{view})
	return func() { stop(); unregister() }
}

type sharedBrowserOperator struct{ view *browserhost.RemoteView }

func (a sharedBrowserOperator) SendOperatorInput(ctx context.Context, in BrowserOperatorInput) error {
	return a.view.SendOperatorInput(ctx, browserhost.OperatorInput{BrowserID: in.BrowserID, TabID: in.TabID, TargetID: in.TargetID, Kind: in.Kind, X: in.X, Y: in.Y, DeltaY: in.DeltaY, Text: in.Text, Key: in.Key})
}

func (a sharedBrowserOperator) ChromeAction(ctx context.Context, name string, params any, target browserctl.Target, timeout time.Duration) (json.RawMessage, browserctl.Meta, error) {
	raw, err := json.Marshal(map[string]any{"id": "gui-operator", "action": name, "params": params, "target": target, "timeoutMs": timeout.Milliseconds()})
	if err != nil {
		return nil, browserctl.Meta{}, err
	}
	out, err := a.view.OperatorAction(ctx, raw)
	if err != nil {
		return nil, browserctl.Meta{}, err
	}
	var response struct {
		OK     bool              `json:"ok"`
		Result json.RawMessage   `json:"result"`
		Meta   browserctl.Meta   `json:"meta"`
		Error  *browserctl.Error `json:"error"`
	}
	if err := json.Unmarshal(out, &response); err != nil {
		return nil, browserctl.Meta{}, err
	}
	if !response.OK {
		if response.Error != nil {
			return nil, response.Meta, response.Error
		}
		return nil, response.Meta, fmt.Errorf("operator action failed")
	}
	return response.Result, response.Meta, nil
}
