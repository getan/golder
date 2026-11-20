package computeruse

import "errors"

// StaleReason keeps the fail-closed ErrStaleObservation contract while naming
// the exact pre-dispatch check that failed. It never carries page content,
// other windows' titles or accessibility values.
type StaleReason struct {
	Code    string
	Message string
}

func (e *StaleReason) Error() string {
	message := "observation is stale"
	if e.Code != "" {
		message += " (" + e.Code + ")"
	}
	if e.Message != "" {
		message += ": " + e.Message
	}
	return message
}

// Unwrap keeps errors.Is(err, ErrStaleObservation) true for every existing
// caller and test.
func (e *StaleReason) Unwrap() error { return ErrStaleObservation }

func staleReason(code, message string) error {
	return &StaleReason{Code: code, Message: message}
}

// StaleCode returns the structured reason, or "" for a generic stale error.
func StaleCode(err error) string {
	var reason *StaleReason
	if errors.As(err, &reason) {
		return reason.Code
	}
	return ""
}
