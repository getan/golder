package computeruse

import "errors"

// Only an inspected action list without AXConfirm selects the keyboard path.
// In particular an error returned by an attempted AXConfirm is terminal.
var errNoConfirmAction = errors.New("selected field does not advertise AXConfirm")

// Both paths are addressed to the approved target's pid; neither raises or
// switches to its application or desktop.
func routeFieldSubmit(confirm, sendReturn func() error) error {
	if err := confirm(); err != errNoConfirmAction {
		return err
	}
	return sendReturn()
}
