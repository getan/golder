package adb

import "testing"

func validHandoff() Handoff {
	return Handoff{
		CallingUID: 2000, CallingPID: 42,
		ReportedUID: 2000, ReportedPID: 42,
		HelperAPK: "/data/app/golder/base.apk",
		AppAPK:    "/data/app/golder/base.apk",
		Nonce:     "0123456789abcdef",
		Expected:  "0123456789abcdef",
		HasBinder: true,
	}
}

func TestHandoffAcceptsOnlyShellIdentity(t *testing.T) {
	good := validHandoff()
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Handoff){
		"not-shell":    func(h *Handoff) { h.CallingUID = 10123 },
		"reported-pid": func(h *Handoff) { h.ReportedPID++ },
		"stale-apk":    func(h *Handoff) { h.HelperAPK = "/data/app/old/base.apk" },
		"no-binder":    func(h *Handoff) { h.HasBinder = false },
		"bad-nonce":    func(h *Handoff) { h.Nonce = "fedcba9876543210" },
	} {
		changed := validHandoff()
		mutate(&changed)
		if err := changed.Validate(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
