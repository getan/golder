package computeruse

import "errors"

// Only an inspected action list without AXConfirm selects the keyboard path.
// In particular an error returned by an attempted AXConfirm is terminal.
var errNoConfirmAction = errors.New("selected field does not advertise AXConfirm")

func routeFieldSubmit(confirm, foreground, sendReturn func() error) error {
	if err := confirm(); err != errNoConfirmAction {
		return err
	}
	if err := foreground(); err != nil {
		return noInputError(err)
	}
	return sendReturn()
}
