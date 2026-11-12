package agenttool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/browserctl"
)

func mustPersistentBrowserProfile(t *testing.T, root, name string) string {
	t.Helper()
	profile, err := persistentBrowserProfile(root, name, false)
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func TestBrowserProfileConsentIsSessionLocal(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	root := t.TempDir()
	a, b := NewBrowserTool(root, true), NewBrowserTool(root, true)
	raw := json.RawMessage(`{"action":"open","profile":"publishing"}`)
	if !a.RequiresOperatorConfirmationFor(raw) || !b.RequiresOperatorConfirmationFor(raw) {
		t.Fatal("new sessions must ask before using saved logins")
	}
	if err := a.authorizeProfile(context.Background(), "one", raw, "publishing"); err == nil {
		t.Fatal("model arguments granted profile authority")
	}
	ctx := agentcore.WithOperatorConfirmation(context.Background(), "one", "browser", raw)
	if err := a.authorizeProfile(ctx, "wrong-call", raw, "publishing"); err == nil {
		t.Fatal("confirmation was not bound to the exact call")
	}
	if err := a.authorizeProfile(ctx, "one", raw, "publishing"); err != nil {
		t.Fatal(err)
	}
	if a.RequiresOperatorConfirmationFor(raw) || !b.RequiresOperatorConfirmationFor(raw) {
		t.Fatal("profile authorization leaked to another agent session")
	}
	info := browserctl.Info{LoginProfile: browserProjectKey(root) + "/publishing"}
	if a.checkProfileAccess(info) != nil || b.checkProfileAccess(info) == nil {
		t.Fatal("saved-login DOM/screenshot access leaked to an unapproved agent")
	}
	if a.checkProfileAccess(browserctl.Info{LoginProfile: "another-project/publishing"}) == nil {
		t.Fatal("same profile name granted access across projects")
	}
	shared := browserctl.Info{LoginProfile: browserLoginProfile(root, sharedBrowserProfileName)}
	if a.checkProfileAccess(shared) != nil || b.checkProfileAccess(shared) != nil || !a.Control.profileAllowed(sharedBrowserProfileName) {
		t.Fatal("project default profile was not shared across agent sessions")
	}
	if a.RequiresOperatorConfirmationFor(json.RawMessage(`{"action":"open","profile":"default"}`)) {
		t.Fatal("project default profile unexpectedly requires named-profile consent")
	}
	p, err := persistentBrowserProfile(root, "publishing", true)
	if err != nil {
		t.Fatal(err)
	}
	q, err := persistentBrowserProfile(root, "publishing", false)
	if err != nil || p != q || insidePath(root, p) {
		t.Fatalf("profile must be stable and outside repository data: %q %q %v", p, q, err)
	}
	if filepath.Base(p) != "publishing.profile" {
		t.Fatal("profile name changed across restarts")
	}
	for _, bad := range []string{"../personal", "/tmp/profile", ".", "two words", "", "publish.profile"} {
		if _, err := persistentBrowserProfile(root, bad, false); err == nil {
			t.Errorf("accepted invalid profile name %q", bad)
		}
	}
	if !a.RequiresOperatorConfirmationFor(json.RawMessage(`{"action":"resume"}`)) {
		t.Fatal("automatic reviewer must not resume paused browser actions")
	}
	// A profile must not follow a link to a person's ordinary browser data.
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), p); err != nil {
		t.Fatal(err)
	}
	if _, err := persistentBrowserProfile(root, "publishing", true); err == nil {
		t.Fatal("followed a profile symlink")
	}
}

func TestSavedBrowserProfileAdoptionNeedsExactConsent(t *testing.T) {
	root := t.TempDir()
	tool := NewBrowserTool(root, true)
	instance := browserInstance{Socket: filepath.Join(root, "browser.sock"), Info: browserctl.Info{
		BrowserID: "saved-browser", LoginProfile: browserProjectKey(root) + "/publishing",
	}}
	tool.discover = func(context.Context) browserDiscovery {
		return browserDiscovery{Instances: []browserInstance{instance}}
	}
	raw, err := tool.PrepareCall(context.Background(), json.RawMessage(`{"action":"open","profile":"publishing"}`))
	if err != nil {
		t.Fatal(err)
	}
	ctx := agentcore.WithNetworkGrant(context.Background())
	if result, _ := tool.Execute(ctx, "one", raw, nil); !result.IsError {
		t.Fatal("another session's browser was adopted without consent")
	}
	// Profile consent does not create ownership. This branch exercises reuse
	// of a view already registered by this session's trusted launcher.
	tool.Control.rememberNativeBrowser(instance.Info.BrowserID, instance.Socket, "publishing", browserProxyModeDirect)
	ctx = agentcore.WithOperatorConfirmation(ctx, "one", "browser", raw)
	if result, _ := tool.Execute(ctx, "one", raw, nil); result.IsError {
		t.Fatalf("exact consent did not allow reuse: %+v", result)
	}
	if err := tool.Control.checkBrowser(context.Background(), instance.Info.BrowserID, instance.Socket, false); err != nil {
		t.Fatal("approved profile access did not remain scoped to this agent session", err)
	}
	other := NewBrowserTool(root, true)
	if other.checkProfileAccess(instance.Info) == nil {
		t.Fatal("profile consent leaked to another pane/agent")
	}
}

func TestSharedDefaultBrowserAdoptionNeedsNoNamedProfileConsent(t *testing.T) {
	root := t.TempDir()
	instance := browserInstance{Socket: filepath.Join(root, "browser.sock"), Info: browserctl.Info{
		BrowserID: "shared-browser", LoginProfile: browserLoginProfile(root, sharedBrowserProfileName),
	}}
	for _, tool := range []*BrowserTool{NewBrowserTool(root, true), NewBrowserTool(root, true)} {
		tool.Control.rememberNativeBrowser(instance.Info.BrowserID, instance.Socket, sharedBrowserProfileName, browserProxyModeDirect)
		tool.discover = func(context.Context) browserDiscovery {
			return browserDiscovery{Instances: []browserInstance{instance}}
		}
		raw, err := tool.PrepareCall(context.Background(), json.RawMessage(`{"action":"open"}`))
		if err != nil {
			t.Fatal(err)
		}
		if tool.RequiresOperatorConfirmationFor(raw) {
			t.Fatal("shared default browser requested named-profile confirmation")
		}
		ctx := agentcore.WithNetworkGrant(context.Background())
		if result, _ := tool.Execute(ctx, "open", raw, nil); result.IsError {
			t.Fatalf("agent session could not adopt shared default browser: %+v", result)
		}
		if err := tool.Control.checkBrowser(context.Background(), instance.Info.BrowserID, instance.Socket, false); err != nil {
			t.Fatal("shared default browser was not remembered by this agent session:", err)
		}
	}
}

func TestAuthorizedNamedProfileStaysSeparateFromSharedDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GOLDER_HOME", home)
	root := shortRoot(t)
	if err := ConfigureEmbeddedBrowser(os.Args[0]); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ConfigureEmbeddedBrowser("") })
	guard := NewControlGuard(root)
	guard.browserProfiles = map[string]bool{"publishing": true}
	spec, err := controlledBrowserSpecWithProfile(guard, root, "browser", "about:blank", "publishing")
	if err != nil {
		t.Fatal(err)
	}
	want, err := persistentBrowserProfile(root, "publishing", false)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Profile != want || !slices.Contains(spec.Argv, "-profile-dir") || slices.Contains(spec.Argv, "-private") {
		t.Fatalf("named login profile was not persistent: %+v", spec)
	}
	shared, err := persistentBrowserProfile(root, sharedBrowserProfileName, false)
	if err != nil {
		t.Fatal(err)
	}
	if shared == spec.Profile {
		t.Fatal("named login profile reused the project shared profile")
	}
	if !strings.HasPrefix(spec.Profile, filepath.Join(home, "browser-profiles")+string(filepath.Separator)) {
		t.Fatalf("named profile escaped private storage: %q", spec.Profile)
	}
	if spec.StartupReport == "" || filepath.Dir(spec.StartupReport) != browserStateDir(root) {
		t.Fatalf("startup report should stay out of the login profile: %+v", spec)
	}
}
