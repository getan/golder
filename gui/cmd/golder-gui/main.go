// Command golder-gui is the Golder desktop app: MyGo's native UI over the same
// run, session and permission layers the CLI and TUI use.
//
//	golder-gui [-cwd DIR] [-model ID] [-provider NAME] [-resume ID] [-approve]
//
// It is a separate binary. The `golder` CLI/TUI never imports MyGo and is not
// affected by this command's dependencies.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/getan/golder/gui/appsession"
	"github.com/getan/golder/gui/mygoui"
	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/cli/config"
	"github.com/getan/golder/internal/cli/run"
	"github.com/getan/golder/internal/runtime"
)

func main() {
	var (
		cwd          = flag.String("cwd", "", "project directory to run the agent in")
		model        = flag.String("model", "", "model id (default: config.toml, else the runtime default)")
		providerName = flag.String("provider", "", "provider name")
		baseURL      = flag.String("base-url", "", "provider endpoint override")
		protocol     = flag.String("protocol", "", "wire protocol override")
		apiKey       = flag.String("api-key", "", "API key override (prefer the provider's environment variable)")
		resume       = flag.String("resume", "", "session id to resume")
		approve      = flag.Bool("approve", false, "trust the project directory for this run")
		noTools      = flag.Bool("no-tools", false, "start without tools")
		noSkills     = flag.Bool("no-skills", false, "start without skills")
	)
	flag.Parse()

	if strings.TrimSpace(*cwd) != "" {
		if err := os.Chdir(*cwd); err != nil {
			log.Fatalf("golder-gui: %v", err)
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		log.Fatalf("golder-gui: %v", err)
	}

	cfg, cfgErr := config.LoadFileConfig(config.FileConfigPath())
	if cfgErr != nil {
		// A malformed config is reported, not silently ignored; the app still
		// starts with explicit flags and built-in defaults.
		fmt.Fprintf(os.Stderr, "golder-gui: %v\n", cfgErr)
		cfg = config.FileConfig{}
	}

	modelID := firstNonEmpty(*model, cfg.Model)
	provider := firstNonEmpty(*providerName, cfg.Provider)
	base := firstNonEmpty(*baseURL, cfg.BaseURL)
	proto := firstNonEmpty(*protocol, cfg.Protocol)
	key := firstNonEmpty(*apiKey, cfg.APIKey)
	if modelID != "" {
		modelID = providerCanonical(modelID)
	}
	defaults := runtime.DefaultConfigLayer()
	if modelID == "" && defaults.Model != nil {
		modelID = *defaults.Model
	}
	thinking := thinkingLevel(firstNonEmpty(cfg.ThinkingLevel))
	if thinking == "" && defaults.ThinkingLevel != nil {
		thinking = agentcore.ThinkingLevel(*defaults.ThinkingLevel)
	}
	maxContext, _ := cfg.Compaction.ResolveMaxContext()
	memoryEnabled := cfg.Memory.Resolve().Enabled

	env, err := run.SetupEnv(modelID, base, proto, provider, key, *noTools, *noSkills, "", nil, memoryEnabled, run.ToolPolicy{})
	if err != nil {
		log.Fatalf("golder-gui: %v", err)
	}
	if env.Extensions != nil {
		defer env.Extensions.Close()
	}
	if env.Memory != nil {
		defer env.Memory.Close()
	}

	ctrl := appsession.New(appsession.Options{
		Model:         modelID,
		ProviderName:  env.ProviderName,
		Provider:      env.Provider,
		BaseURL:       base,
		APIKey:        key,
		Protocol:      proto,
		ThinkingLevel: thinking,
		Tools:         env.Tools,
		SysPrompt:     env.SysPrompt,
		Prompt:        env.Prompt,
		ResumeID:      *resume,
		Approve:       *approve,
		Skills:        env.Skills,
		Extensions:    env.Extensions,
		ConfigPrompts: cfg.Prompts,
		Permissions:   env.Permissions,
		ReviewNotes:   env.ReviewNotes,
		Budget:        env.Budget,
		MaxContext:    maxContext,
		Cwd:           wd,
		Warn:          os.Stderr,
	})
	if err := ctrl.Open(context.Background()); err != nil {
		log.Fatalf("golder-gui: %v", err)
	}
	defer ctrl.Close()

	if err := mygoui.New(ctrl).Run(); err != nil {
		log.Fatalf("golder-gui: %v", err)
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func providerCanonical(id string) string {
	return id
}

func thinkingLevel(s string) agentcore.ThinkingLevel {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "minimal":
		return agentcore.ThinkingMinimal
	case "low":
		return agentcore.ThinkingLow
	case "medium":
		return agentcore.ThinkingMedium
	case "high":
		return agentcore.ThinkingHigh
	case "xhigh":
		return agentcore.ThinkingXHigh
	case "max":
		return agentcore.ThinkingMax
	case "off":
		return agentcore.ThinkingOff
	default:
		return ""
	}
}
