//go:build darwin

package agenttool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/seatbelt"
	"github.com/getan/golder/internal/toolchain"
)

// This opt-in acceptance test uses the installed SDK and default Go caches.
// It does not redirect GOCACHE/GOMODCACHE/GOPATH or grant all of HOME.
func TestBashGoProfileNative(t *testing.T) {
	if os.Getenv("GOLDER_GO_PROFILE_TEST") != "1" {
		t.Skip("set GOLDER_GO_PROFILE_TEST=1 for native contained Go acceptance")
	}
	if !seatbelt.Available() {
		t.Skip("sandbox-exec unavailable")
	}
	project := t.TempDir()
	p, err := toolchain.Resolve(context.Background(), project, "go")
	if err != nil {
		t.Fatal(err)
	}
	// The private fixture lives in the checkout, outside the temp project and
	// outside the OS's default readable roots. It contains no operator data.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	private, err := os.MkdirTemp(cwd, ".profile-private-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(private)
	if err := os.WriteFile(filepath.Join(private, "private.txt"), []byte("fixture must remain unreadable"), 0600); err != nil {
		t.Fatal(err)
	}
	source := fmt.Sprintf(`package acceptance
import ("os"; "testing")
func TestBoundaries(t *testing.T) {
 if _,err:=os.ReadDir(%q);err!=nil {t.Fatalf("module-cache read: %%v",err)}
 if _,err:=os.ReadFile(%q);!os.IsPermission(err) {t.Fatalf("ungranted fixture not denied: %%v",err)}
}
`, p.ReadDirs[1], filepath.Join(private, "private.txt"))
	for name, text := range map[string]string{"go.mod": "module example.invalid/profileacceptance\n\ngo 1.20\n", "acceptance_test.go": source} {
		if err := os.WriteFile(filepath.Join(project, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	tool := &BashTool{Dir: project, Sandbox: seatbelt.New(project), Permissions: permissions.New(permissions.Auto)}
	defer tool.StopSessions()
	ctx := agentcore.WithToolchainGrant(context.Background(), agentcore.ToolchainGrant{Name: p.Name, Binding: p.Binding, Executable: p.Executable, GoRoot: p.GoRoot, ReadDirs: p.ReadDirs, WriteDirs: p.WriteDirs, ReadFiles: p.ReadFiles})
	args, _ := json.Marshal(map[string]any{"command": "go test -count=1 ./...", "profile": "go", "yield_time_ms": 30000, "timeout_ms": 90000})
	result, err := tool.Execute(ctx, "native-profile", args, nil)
	text := agentcore.ContentToText(result.Content)
	if strings.Contains(text, "sandbox_apply: Operation not permitted") {
		t.Skip("outer sandbox forbids a nested OS sandbox")
	}
	if err != nil || result.IsError || !strings.Contains(text, "ok ") || result.Details.(map[string]any)["envelope"] != "sandbox" {
		t.Fatalf("contained Go acceptance failed: result=%+v err=%v", result, err)
	}
	t.Log("installed Go built and tested using default caches; ungranted checkout fixture was denied")
}
