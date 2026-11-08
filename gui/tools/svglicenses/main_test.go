package main

import "testing"

func TestPermissiveLicense(t *testing.T) {
	for _, expr := range []string{"MIT", "MIT OR Apache-2.0", "(MIT OR Apache-2.0) AND Unicode-3.0", "MIT/Apache-2.0", "Zlib"} {
		if !permissiveLicense(expr) {
			t.Errorf("permissive expression rejected: %q", expr)
		}
	}
	for _, expr := range []string{"", "!!!", "MPL-2.0", "GPL-3.0-only", "MIT OR LGPL-2.1-only", "LicenseRef-Custom"} {
		if permissiveLicense(expr) {
			t.Errorf("unreviewed expression accepted: %q", expr)
		}
	}
}
