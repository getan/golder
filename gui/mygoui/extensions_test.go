package mygoui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/appsession"
)

func TestExtensionSetupAuthorizationURLLivesOnlyWhilePending(t *testing.T) {
	a := testApp()
	a.extensionBusy = true
	url := "https://accounts.feishu.cn/device?user_code=public"
	if opened := a.applyExtensionSetupEvent(appsession.ExtensionSetupEvent{Type: "authorization", VerificationURL: url}); opened != url || a.extensionAuthURL != url {
		t.Fatalf("authorization event did not retain the active URL: opened=%q state=%q", opened, a.extensionAuthURL)
	}
	a.applyExtensionSetupEvent(appsession.ExtensionSetupEvent{
		Type: "failure", Phase: "verify_authorization", Code: "AUTH_VERIFY_NETWORK_FAILED",
		Message: "Authorization may have been saved.", Retryable: true,
	})
	if a.extensionAuthURL != "" || a.extensionErr == "" || !strings.Contains(a.extensionErr, "AUTH_VERIFY_NETWORK_FAILED") {
		t.Fatalf("failure retained stale URL or lost safe diagnostics: url=%q err=%q", a.extensionAuthURL, a.extensionErr)
	}
	a.extensionAuthURL = url
	a.applyExtensionSetupEvent(appsession.ExtensionSetupEvent{Type: "complete", Authenticated: true})
	if a.extensionAuthURL != "" || a.extensionErr != "" || a.extensionNote != "Authorization verified." {
		t.Fatalf("completion state: url=%q err=%q note=%q", a.extensionAuthURL, a.extensionErr, a.extensionNote)
	}
}

func TestExpiredAuthorizationURLButtonIsHidden(t *testing.T) {
	a := testApp()
	a.settingsOpen = true
	a.settingsTab = 2
	a.extension = appsession.LarkExtensionSettings{
		ExtensionPackageView: appsession.ExtensionPackageView{
			Installed: true, Configured: true, Enabled: true, Version: "1.5.54", Authentication: "required",
		},
		AppID: "cli_test", CredentialAvailable: true,
	}
	a.extensionAuthURL = "https://accounts.feishu.cn/device?expired=true"
	a.extensionBusy = false
	tt := ui.NewTester(a.view, 1200, 800)
	if tt.HasText("Open authorization page again") {
		t.Fatal("finished authorization still exposed its stale URL")
	}
	for _, want := range []string{"Application: Configured", "User authorization: Required"} {
		if !tt.HasText(want) {
			t.Fatalf("missing %q; texts=%q", want, tt.Texts())
		}
	}
}

func TestLarkStatusAndVersionLabelsAreIndependent(t *testing.T) {
	configured := appsession.LarkExtensionSettings{
		ExtensionPackageView: appsession.ExtensionPackageView{Configured: true, Authentication: "verified"},
		AppID:                "cli_test", CredentialAvailable: true,
	}
	if got := larkApplicationStatus(configured); got != "Configured" {
		t.Fatalf("application status=%q", got)
	}
	if got := larkAuthorizationStatus(configured.Authentication); got != "Verified" {
		t.Fatalf("authorization status=%q", got)
	}
	if got := larkAuthorizationStatus("configured"); got != "Not verified" {
		t.Fatalf("configured env was presented as user auth: %q", got)
	}
	if warning := extensionVersionWarning("v1.5.54", "1.5.53"); !strings.Contains(warning, "differ") {
		t.Fatalf("version mismatch warning=%q", warning)
	}
	if warning := extensionVersionWarning("v1.5.54", "1.5.54"); warning != "" {
		t.Fatalf("matching versions warned: %q", warning)
	}
}
