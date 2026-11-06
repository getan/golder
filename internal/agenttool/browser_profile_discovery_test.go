package agenttool

import (
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/browserctl"
)

func TestSavedProfileDiscoveryDoesNotExposeSignedInPages(t *testing.T) {
	root := t.TempDir()
	tool := NewBrowserTool(root, true)
	instance := browserInstance{Selected: true, Info: browserctl.Info{
		BrowserID: "saved", LoginProfile: browserProjectKey(root) + "/publishing",
		Tabs:              []browserctl.Tab{{ID: "private-tab", URL: "https://private.example/account", Title: "Private account"}},
		ExplicitSelection: &browserctl.ExplicitSelection{Revision: 1, TabID: "private-tab", TargetID: "private-target"},
	}}
	d := browserDiscovery{Instances: []browserInstance{instance}}
	result := tool.listInstancesResult(d)
	text := result.Content[0].(agentcore.TextContent).Text
	if strings.Contains(text, "private.example") || strings.Contains(text, "Private account") || strings.Contains(text, "private-tab") || strings.Contains(text, "private-target") {
		t.Fatal("discovery leaked signed-in page metadata")
	}
	if len(d.Instances[0].Info.Tabs) != 1 || d.Instances[0].Info.ExplicitSelection == nil {
		t.Fatal("redaction changed the live discovery snapshot")
	}
	tool.Control.browserProfiles = map[string]bool{"publishing": true}
	result = tool.listInstancesResult(d)
	if !strings.Contains(result.Content[0].(agentcore.TextContent).Text, "private.example") {
		t.Fatal("approved agent lost its page inventory")
	}
}
