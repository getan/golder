package computeruse

import "errors"

// Only an inspected action list without AXConfirm selects the keyboard path.
// In particular an error returned by an attempted AXConfirm is terminal.
var errNoConfirmAction = errors.New("selected field does not advertise AXConfirm")

// Keyboard first: key delivery is addressed to the target pid, so it works
// while the operator keeps Golder in front. Foreground is only for the proven
// case that the field itself lacks keyboard focus.
func routeFieldSubmit(confirm, sendReturn, foreground func() error) error {
	if err := confirm(); err != errNoConfirmAction {
		return err
	}
	err := sendReturn()
	if err == nil || StaleCode(err) != "field_focus_unconfirmed" {
		return err
	}
	if err := foreground(); err != nil {
		return noInputError(err)
	}
	return sendReturn()
}
