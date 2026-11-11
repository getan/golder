package mygoui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/getan/golder/gui/appsession"
)

func TestLarkPersonalAuthorizationUsesServiceStatusNotMCPAuthentication(t *testing.T) {
	for _, tc := range []struct {
		name, transport, lark, project, larkLabel, projectLabel string
	}{
		{"configured source with saved logins", "configured", "authorized", "authorized", "Authorized", "Authorized"},
		{"stale transport requirement", "required", "authorized", "authorized", "Authorized", "Authorized"},
		{"transport verification is not user verification", "verified", "", "", "Not checked", "Not checked"},
		{"unavailable check is not logged out", "configured", "unknown", "unknown", "Could not determine; retry", "Could not determine; retry"},
		{"independent grants", "verified", "authorized", "required", "Authorized", "Authorization required"},
		{"optional Project", "not-required", "authorized", "unavailable", "Authorized", "Not configured"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := testApp()
			a.extension = appsession.LarkExtensionSettings{
				ExtensionPackageView: appsession.ExtensionPackageView{
					Installed: true, Configured: true, Enabled: true, Authentication: tc.transport,
				},
				AppID: "cli_test", CredentialAvailable: true,
			}
			a.extensionLarkStatus, a.extensionProjectStatus = tc.lark, tc.project
			tt := ui.NewTester(a.larkExtensionCard, 1000, 1400)
			for _, want := range []string{"Application: Configured", "Lark authorization: " + tc.larkLabel, "Project authorization: " + tc.projectLabel} {
				if !tt.HasText(want) {
					t.Fatalf("missing service status %q: %q", want, tt.Texts())
				}
			}
			for _, text := range tt.Texts() {
				if strings.HasPrefix(text, "User authorization:") {
					t.Fatalf("MCP transport authentication was mislabeled as personal authorization: %q", text)
				}
			}
		})
	}
}
