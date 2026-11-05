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
	if opened := a.applyExtensionSetupEvent(appsession.ExtensionSetupEvent{Type: "authorization", VerificationURL: url, UserCode: "ABCD-1234"}); opened != url || a.extensionAuthURL != url || a.extensionAuthCode != "ABCD-1234" {
		t.Fatalf("authorization event did not retain the active URL: opened=%q state=%q", opened, a.extensionAuthURL)
	}
	a.applyExtensionSetupEvent(appsession.ExtensionSetupEvent{
		Type: "failure", Phase: "verify_authorization", Code: "AUTH_VERIFY_NETWORK_FAILED",
		Message: "Authorization may have been saved.", Retryable: true,
	})
	if a.extensionAuthURL != "" || a.extensionAuthCode != "" || a.extensionErr == "" || !strings.Contains(a.extensionErr, "AUTH_VERIFY_NETWORK_FAILED") {
		t.Fatalf("failure retained stale URL or lost safe diagnostics: url=%q err=%q", a.extensionAuthURL, a.extensionErr)
	}
	a.extensionAuthURL = url
	a.applyExtensionSetupEvent(appsession.ExtensionSetupEvent{Type: "complete", Authenticated: true})
	if a.extensionAuthURL != "" || a.extensionErr != "" || a.extensionNote != "Authorization verified." {
		t.Fatalf("completion state: url=%q err=%q note=%q", a.extensionAuthURL, a.extensionErr, a.extensionNote)
	}
}

func TestExtensionSetupFailureMatchesSavedStatus(t *testing.T) {
	event := appsession.ExtensionSetupEvent{
		Type: "failure", Phase: "wait_for_authorization", Code: "AUTH_POLL_FAILED",
		Message: "Authorization did not complete. Start a new authorization flow.", Retryable: true,
	}
	want := "Authorization did not complete. Start a new authorization flow. [AUTH_POLL_FAILED · wait_for_authorization] Retry is safe."
	if got := extensionSetupFailureText(event); got != want {
		t.Fatalf("failure=%q, want %q", got, want)
	}
}

func TestProjectAuthorizationUsesSavedReviewedConfiguration(t *testing.T) {
	settings := appsession.LarkExtensionSettings{SavedProjectCLI: "/opt/meegle", SavedProjectHost: "project.feishu.cn"}
	edit := appsession.LarkExtensionEdit{ProjectCLI: "/opt/meegle", ProjectHost: "project.feishu.cn"}
	if !projectAuthorizationConfigured(settings, edit) {
		t.Fatal("saved Project configuration was rejected")
	}
	edit.ProjectCLI = "/opt/another-meegle"
	if projectAuthorizationConfigured(settings, edit) {
		t.Fatal("unsaved Project executable was treated as approved")
	}
	edit.ProjectCLI, edit.ProjectHost = "/opt/meegle", "other.example"
	if projectAuthorizationConfigured(settings, edit) {
		t.Fatal("unsaved Project host was treated as approved")
	}
	settings.SavedProjectCLI = ""
	if projectAuthorizationConfigured(settings, appsession.LarkExtensionEdit{ProjectCLI: "/opt/meegle"}) {
		t.Fatal("discovered but unsaved Project executable was treated as configured")
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
	mismatch := configured
	mismatch.BundledVersion, mismatch.InstalledVersion, mismatch.ActiveVersion = "1.5.54", "1.5.54", "1.5.53"
	mismatch.UpdateAvailable = true
	if larkHelperReady(mismatch, "v1.5.54") {
		t.Fatal("mismatched active helper was treated as ready")
	}
	matching := configured
	matching.BundledVersion, matching.InstalledVersion, matching.ActiveVersion = "1.5.54", "1.5.54", "1.5.54"
	matching.ActiveManaged = true
	if !larkHelperReady(matching, "v1.5.54") {
		t.Fatal("matching helper was not ready")
	}
	bundled, installed, active := larkHelperVersions(mismatch, "v1.5.54")
	if strings.Join([]string{bundled, installed, active}, "/") != "1.5.54/1.5.54/1.5.53" {
		t.Fatalf("versions=%s/%s/%s", bundled, installed, active)
	}
}

func TestLarkHelperMismatchShowsOneClickUpdate(t *testing.T) {
	a := testApp()
	a.settingsOpen = true
	a.settingsTab = 2
	a.extension = appsession.LarkExtensionSettings{
		ExtensionPackageView: appsession.ExtensionPackageView{
			Installed: true, Configured: true, Enabled: true,
			BundledVersion: "1.5.55", InstalledVersion: "1.5.55", ActiveVersion: "1.5.54",
			Version: "1.5.54", UpdateAvailable: true, State: "disconnected", Authentication: "configured",
		},
		AppID: "cli_test", CredentialName: "lark-app-secret", CredentialAvailable: true,
	}
	a.extensionEdit = appsession.LarkExtensionEdit{AppID: "cli_test", CredentialName: "lark-app-secret"}
	a.extensionDomains = map[string]bool{"docs": true}
	tt := ui.NewTester(a.view, 1200, 800)
	for _, want := range []string{
		"Bundled: 1.5.55", "Installed latest: 1.5.55", "Active Source: 1.5.54",
		"Update helper", "Update the managed helper before authorization.",
	} {
		if !tt.HasText(want) {
			t.Fatalf("mismatch UI is missing %q; texts=%q", want, tt.Texts())
		}
	}
	if larkHelperReady(a.extension, "1.5.55") {
		t.Fatal("mismatched helper left authorization controls ready")
	}
}
