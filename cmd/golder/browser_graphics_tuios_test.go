//go:build darwin || linux

package main

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// This exercises the actual pinned tuios-mini renderer in process, without a
// PTY or browser. Its module lives in the immutable module cache, so the test
// aliases it under t.TempDir and overlays only the test fixture. It is opt-in
// because it compiles the upstream package's test suite in a child Go process.
func TestBrowserFrameAgainstTuiosWorkspaceIsolation(t *testing.T) {
	if os.Getenv("GOLDER_TEST_TUIOS_GRAPHICS") != "1" {
		t.Skip("set GOLDER_TEST_TUIOS_GRAPHICS=1 to exercise the pinned tuios renderer")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	env := browserGraphicsTestEnv()
	modcmd := exec.CommandContext(ctx, "go", "list", "-m", "-f", "{{.Dir}}", "github.com/getan/tuios-mini")
	modcmd.Dir, modcmd.Env = root, env
	dir, err := modcmd.Output()
	if err != nil {
		t.Fatalf("locate pinned tuios module: %v", err)
	}
	tmp := t.TempDir()
	alias := filepath.Join(tmp, "tuios-mini")
	if err := os.Symlink(strings.TrimSpace(string(dir)), alias); err != nil {
		t.Fatal(err)
	}
	mod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	mod = []byte(strings.ReplaceAll(string(mod), "=> ./third_party/ultraviolet", "=> "+strconv.Quote(filepath.Join(root, "third_party/ultraviolet"))))
	mod = fmt.Appendf(mod, "\nreplace github.com/getan/tuios-mini => %s\n", strconv.Quote(alias))
	modfile := filepath.Join(tmp, "regression.mod")
	if err := os.WriteFile(modfile, mod, 0o600); err != nil {
		t.Fatal(err)
	}
	sum, err := os.ReadFile(filepath.Join(root, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "regression.sum"), sum, 0o600); err != nil {
		t.Fatal(err)
	}
	overlay := struct{ Replace map[string]string }{Replace: map[string]string{
		filepath.Join(alias, "internal/app/golder_workspace_graphics_test.go"): filepath.Join(root, "cmd/golder/testdata/tuios_workspace_graphics_test.txt"),
	}}
	data, err := json.Marshal(overlay)
	if err != nil {
		t.Fatal(err)
	}
	overlayFile := filepath.Join(tmp, "overlay.json")
	if err := os.WriteFile(overlayFile, data, 0o600); err != nil {
		t.Fatal(err)
	}
	frame := browserIntegrationFrame(t)
	cmd := exec.CommandContext(ctx, "go", "test", "-race", "-modfile", modfile, "-overlay", overlayFile,
		"github.com/getan/tuios-mini/internal/app", "-run", "^TestGolderBrowserWorkspaceGraphicsIsolation$", "-count=1", "-timeout=90s")
	cmd.Dir = root
	cmd.Env = append(env, "GOLDER_TEST_GRAPHICS_FRAME="+base64.StdEncoding.EncodeToString(frame))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tuios workspace graphics regression: %v\n%s", err, out)
	}
}

func browserIntegrationFrame(t *testing.T) []byte {
	t.Helper()
	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	if _, err := zw.Write([]byte{255, 0, 0, 0, 255, 0, 0, 0, 255, 255, 255, 255}); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	payload := base64.StdEncoding.EncodeToString(compressed.Bytes())
	frame := "\x1b_Ga=T,f=24,o=z,s=2,v=2,c=10,r=5,i=1,p=1,q=2,C=1,m=1;" + payload[:8] + "\x1b\\\x1b_Gm=0;" + payload[8:] + "\x1b\\"
	var out bytes.Buffer
	writer := &browserFrameWriter{out: &out}
	if _, err := writer.Write([]byte(frame)); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func browserGraphicsTestEnv() []string {
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "TUIOS_") || strings.HasPrefix(key, "GOLDER_") || key == "GOWORK" || key == "GOTOOLCHAIN" || key == "GOPROXY" {
			continue
		}
		env = append(env, entry)
	}
	return append(env, "GOWORK=off", "GOTOOLCHAIN=local", "GOPROXY=off")
}
