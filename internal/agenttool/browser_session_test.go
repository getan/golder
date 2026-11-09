package agenttool

import (
	"context"
	"os"
	"slices"
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

func TestConversationBrowsersShareProfileNotSocket(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	root := t.TempDir()
	if err := ConfigureEmbeddedBrowser(os.Args[0]); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ConfigureEmbeddedBrowser("") })
	a, b := NewBrowserTool(root, true), NewBrowserTool(root, true)
	ConfigureBrowserSession([]agentcore.AgentTool{a}, "gui-session-a")
	ConfigureBrowserSession([]agentcore.AgentTool{b}, "tui-session-b")
	sa, err := controlledBrowserSpec(a.Control, root, "browser", "about:blank")
	if err != nil {
		t.Fatal(err)
	}
	sb, err := controlledBrowserSpec(b.Control, root, "browser", "about:blank")
	if err != nil {
		t.Fatal(err)
	}
	if sa.Socket == sb.Socket || sa.Profile != sb.Profile || len(sa.Socket) > 103 || !slices.Contains(sa.Argv, "-golder-session") {
		t.Fatalf("scope mismatch: A=%+v B=%+v", sa, sb)
	}
	for _, spec := range []paneSpec{sa, sb} {
		meta := browserMetadata{Name: spec.RecordName, Socket: spec.Socket, Profile: spec.Profile, PersistentProfile: spec.PersistentProfile, ConversationID: spec.ConversationID, Instance: spec.Instance}
		if err := writeBrowserMetadata(browserMetadataPath(root, spec.RecordName), meta); err != nil {
			t.Fatal(err)
		}
	}
	if got := BrowserSessionSocketPaths(root, "gui-session-a"); len(got) != 1 || got[0] != canonicalBrowserSocket(sa.Socket) {
		t.Fatalf("GUI discovered another session: %q", got)
	}
	if got := BrowserSessionSocketPaths(root, "tui-session-b"); len(got) != 1 || got[0] != canonicalBrowserSocket(sb.Socket) {
		t.Fatalf("TUI discovered another session: %q", got)
	}
	a.Control.selectBrowser(sa.Socket)
	b.Control.selectBrowser(sb.Socket)
	if a.socketPath() != sa.Socket || b.socketPath() != sb.Socket {
		t.Fatal("process-global selection leaked between sessions")
	}
	ConfigureBrowserSession([]agentcore.AgentTool{a}, "gui-session-next")
	if a.socketPath() != "" || b.socketPath() != sb.Socket {
		t.Fatal("switching conversation retained or cleared another view")
	}
	ConfigureBrowserSession([]agentcore.AgentTool{a}, "gui-session-a")
	again, err := controlledBrowserSpec(a.Control, root, "browser", "about:blank")
	if err != nil || again.Socket != sa.Socket {
		t.Fatalf("resume changed its socket: %q %v", again.Socket, err)
	}
	meta := browserMetadata{Name: sa.RecordName, Socket: sb.Socket, Profile: sa.Profile, PersistentProfile: sa.PersistentProfile, ConversationID: sa.ConversationID, Instance: sa.Instance}
	if checkBrowserRecord(root, meta) == nil {
		t.Fatal("forged metadata redirected a conversation to another socket")
	}
	if _, err := ConnectSessionBrowser(context.Background(), root, "gui-session-a", sb.Socket); err == nil {
		t.Fatal("operator connection crossed conversations")
	}
}
