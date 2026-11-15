package adb

import (
	"crypto/subtle"
	"fmt"
)

// Handoff contains only the facts Android exposes to the helper provider.
// UID/PID comparisons use the Binder caller, never the helper's self-report.
type Handoff struct {
	CallingUID  int
	CallingPID  int
	ReportedUID int
	ReportedPID int
	HelperAPK   string
	AppAPK      string
	Nonce       string
	Expected    string
	HasBinder   bool
}

func (h Handoff) Validate() error {
	if h.CallingUID != 2000 {
		return fmt.Errorf("helper caller is not shell uid 2000")
	}
	if h.ReportedUID != h.CallingUID || h.ReportedPID != h.CallingPID {
		return fmt.Errorf("helper uid/pid mismatch")
	}
	if h.HelperAPK == "" || h.HelperAPK != h.AppAPK {
		return fmt.Errorf("helper apk does not match installed app")
	}
	if !h.HasBinder {
		return fmt.Errorf("helper handed off no binder")
	}
	if h.Nonce == "" || h.Expected == "" ||
		subtle.ConstantTimeCompare([]byte(h.Nonce), []byte(h.Expected)) != 1 {
		return fmt.Errorf("helper nonce mismatch")
	}
	return nil
}
