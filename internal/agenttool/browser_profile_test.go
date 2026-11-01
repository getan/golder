package agenttool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/browserctl"
)

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
