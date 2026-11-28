//go:build windows

package run

import (
	"context"
	"testing"

	"github.com/getan/golder/internal/agenttool"
)

func TestNativeLauncherRegistersBrowserTool(t *testing.T) {
	agenttool.ConfigureNativeBrowserLauncher(func(context.Context, agenttool.NativeBrowserRequest) error {
		return nil
	})
	defer agenttool.ConfigureNativeBrowserLauncher(nil)

	tools := BuiltinTools(t.TempDir(), false)
	for _, tool := range tools {
		if tool.Name() == "browser" {
			return
		}
	}
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name())
	}
	t.Fatalf("browser tool missing; tools = %v", names)
}

func TestSetupEnvKeepsBrowserTool(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	agenttool.ConfigureNativeBrowserLauncher(func(context.Context, agenttool.NativeBrowserRequest) error {
		return nil
	})
	defer agenttool.ConfigureNativeBrowserLauncher(nil)

	env, err := SetupEnv(
		"deepseek-v4.1-flash", "", "", "opencode-go", "test-key",
		false, false, "", nil, false, ToolPolicy{},
		SetupOptions{Cwd: t.TempDir(), NoTuios: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer env.Extensions.Close()
	for _, tool := range env.Tools {
		if tool.Name() == "browser" {
			return
		}
	}
	names := make([]string, 0, len(env.Tools))
	for _, tool := range env.Tools {
		names = append(names, tool.Name())
	}
	t.Fatalf("browser tool missing after SetupEnv; tools = %v", names)
}
