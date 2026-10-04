// Package prompts holds the slash-command registry assembly shared by the REPL
// (internal/cli/repl) and the forthcoming TUI (internal/cli/tui). It was sunk
// out of the repl package (#383) so both front-ends wire the same built-in,
// live-state, plugin-declared, prompt-template and skill commands from one
// owner, avoiding drift between the two command surfaces.
//
// The logic here is a verbatim move of repl's former private
// buildSlashRegistry/loadPromptPaths/promptTemplateSources (plus their
// register helpers), exported unchanged so REPL behavior is identical.
package prompts

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/cli"
	"github.com/getan/golder/internal/cli/ui"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/plugin"
	"github.com/getan/golder/internal/provider"
	"github.com/getan/golder/internal/runtime"
)

// PromptTemplateSources carries the prompt-template discovery sources that
// BuildSlashRegistry loads beyond the global ~/.golder/{commands,prompts} dirs.
// Settings is the config.toml `prompts` array (TierSettings); CLI is the
// --prompt-template flag list (TierCLI, wired in #339). Each entry is a file or
// directory (loaded non-recursively). Missing paths are warned and skipped.
type PromptTemplateSources struct {
	Settings []string
	CLI      []string
	// Disable (--no-prompt-templates) turns off all prompt-template discovery
	// (global, project, settings, CLI); built-ins and skills are unaffected.
	Disable bool
	// ProjectDir is the project-local prompts dir (.golder/prompts in the working
	// dir), loaded at the project tier only when ProjectTrusted is true.
	ProjectDir string
	// ProjectTrusted reports whether the working directory is trusted; project
	// templates load only then (mirrors pi: project prompts after the project is
	// trusted).
	ProjectTrusted bool
}

// LoadPromptPaths loads prompt templates from each path (file or dir), skipping
// and warning on paths that don't exist or fail to read. It is tier-agnostic;
// the caller registers each result at the desired tier (AddSettings/AddCLI).
func LoadPromptPaths(paths []string) []runtime.SlashCommand {
	var out []runtime.SlashCommand
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "golder: prompts path %q not found, skipping\n", p)
			continue
		}
		var cmds []runtime.SlashCommand
		if info.IsDir() {
			cmds, err = runtime.LoadUserCommandsDir(p)
		} else {
			c, e := runtime.LoadPromptFile(p)
			if e != nil {
				err = e
			} else {
				cmds = []runtime.SlashCommand{c}
			}
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "golder: prompts path %q: %v\n", p, err)
			continue
		}
		out = append(out, cmds...)
	}
	return out
}

// BuildSlashRegistry assembles the slash-command registry: compile-time
// built-ins seeded by runtime.NewSlashRegistry, the live-state action commands
// (/model, /help) bound to live, user declarative templates loaded from
// ~/.golder/commands (or $GOLDER_HOME/commands), plugin-declared commands from the
// loaded Manager, plus the pre-loaded skills — each surfaced as a "/skill-name"
// command (mirrors Claude Code's /skill invocation). A missing directory is not an
// error. Names that collide with a built-in are shadowed (the built-in wins) and
// reported on stderr. The skills slice is loaded once by setupAgentEnv (empty
// under --no-skills), so no /skill-name commands are registered when it is
// empty. mgr may be nil (no plugins loaded). creds may be nil, which disables
// "/model" live catalog discovery.
func BuildSlashRegistry(live *cli.LiveConfig, creds *provider.CredentialStore, skills []*runtime.Skill, mgr *plugin.Manager, srcs PromptTemplateSources) (*runtime.SlashRegistry, error) {
	reg := runtime.NewSlashRegistry()
	RegisterLiveCommands(reg, live, creds)
	RegisterPluginCommands(reg, mgr)
	// --no-prompt-templates disables all prompt-template discovery (global,
	// settings, CLI); built-in slash commands and skills are unaffected.
	if !srcs.Disable {
		dir := os.Getenv("GOLDER_HOME")
		if dir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return reg, nil // built-ins only
			}
			dir = filepath.Join(home, ".golder")
		}
		// Load user prompt templates from both the legacy ~/.golder/commands and
		// the pi-aligned ~/.golder/prompts (both non-recursive, global tier).
		// Loading commands first means a same-named template in prompts/
		// overrides the legacy one (last-write-wins within the global tier). A
		// missing directory is not an error (LoadUserCommandsDir returns nil,
		// nil for IsNotExist).
		for _, sub := range []string{"commands", "prompts"} {
			cmds, err := runtime.LoadUserCommandsDir(filepath.Join(dir, sub))
			if err != nil {
				return reg, err
			}
			for _, c := range cmds {
				reg.AddUser(c)
			}
		}
		// Settings-tier templates from the config.toml `prompts` array, then
		// CLI-tier templates from --prompt-template. Each entry is a file or
		// dir; missing paths are warned and skipped.
		for _, c := range LoadPromptPaths(srcs.Settings) {
			reg.AddSettings(c)
		}
		for _, c := range LoadPromptPaths(srcs.CLI) {
			reg.AddCLI(c)
		}
		// Project-tier templates from .golder/prompts in the working directory,
		// loaded only when the project is trusted (mirrors pi). A missing dir is
		// not an error. Overrides global/settings/CLI (project tier is higher).
		if srcs.ProjectTrusted && srcs.ProjectDir != "" {
			cmds, err := runtime.LoadUserCommandsDir(srcs.ProjectDir)
			if err != nil {
				return reg, err
			}
			for _, c := range cmds {
				reg.AddProject(c)
			}
		}
	}
	// Register skills as /skill-name commands from the pre-loaded set (shared with
	// prompt injection in setupAgentEnv, so the directory is read once). All
	// skills — including disable-model-invocation ones — get a slash command; the
	// prompt-injection side filters the disabled ones. Under --no-skills the set
	// is empty, so nothing is registered.
	for _, s := range skills {
		reg.AddSkill(s.SlashCommand())
	}
	if sh := reg.Shadowed(); len(sh) > 0 {
		parts := make([]string, len(sh))
		for i, e := range sh {
			parts[i] = e.String()
		}
		fmt.Fprintf(os.Stderr, "golder: commands shadowed by higher-priority source (rename to use): %v\n", parts)
	}
	return reg, nil
}

// RegisterPluginCommands installs each plugin-declared slash command
// (Manager.Commands()) into the registry as a hybrid (Run) command. Invoking it
// RPCs the owning plugin (Plugin.CallCommand), returns the plugin's
// notifications as the outcome Message, and returns the plugin's Prompt to run
// as the next turn. Plugin commands are registered with AddPlugin so a same-named
// built-in still wins (existing precedence preserved) and a collision is
// reported as shadowed. mgr may be nil (no plugins), in which case this is a
// no-op.
//
// The args passed to CallCommand are the invocation's raw argument text encoded
// as a JSON string (json.RawMessage of a quoted string), never null: the host
// (node #263) expects a JSON string for a no-arg command, so a bare "/cmd"
// sends `""` rather than nil. Each command captures its own plugin and spec name
// (loop variables copied per-iteration).
func RegisterPluginCommands(reg *runtime.SlashRegistry, mgr *plugin.Manager) {
	if mgr == nil {
		return
	}
	for _, pc := range mgr.Commands() {
		pc := pc // capture per iteration
		reg.AddPlugin(runtime.SlashCommand{
			Name:        pc.Spec.Name,
			Description: pc.Spec.Description,
			Run: func(args string) (message, prompt string) {
				// Encode the raw arg text as a JSON string ("" for no args), matching
				// the host's CommandCallParams.Args contract (a JSON string, never
				// null). json.Marshal of a Go string always succeeds.
				raw, _ := json.Marshal(args)
				res, err := pc.Plugin.CallCommand(context.Background(), pc.Spec.Name, json.RawMessage(raw))
				if err != nil {
					return fmt.Sprintf("plugin command %q failed: %v", pc.Spec.Name, err), ""
				}
				return formatNotifications(res.Notifications), res.Prompt
			},
		})
	}
}

// formatNotifications renders a plugin command's notifications into a single
// block to surface to the user, one per line, prefixed by their type (when set)
// so severity is visible. Returns "" when there are none.
// modelCatalogKey resolves the cache key for the live provider's catalog
// (provider name + effective endpoint), or ok=false when the provider is
// unknown (nothing to key on).
func modelCatalogKey(live *cli.LiveConfig) (string, bool) {
	spec, ok := provider.LookupProviderSpec(live.ProviderName)
	if !ok {
		return "", false
	}
	baseURL := provider.ResolveBaseURL(spec, live.BaseURL, os.Getenv)
	return provider.ModelCatalogCacheKey(live.ProviderName, baseURL), true
}

// CachedModelCatalog returns the live model catalog without touching the
// network: the session's already-fetched list when present, else a fresh
// on-disk entry (younger than the shared catalog TTL). A hit is marked on live
// so the rest of the session reuses it. Nil when neither cache has anything.
func CachedModelCatalog(live *cli.LiveConfig) []string {
	key, ok := modelCatalogKey(live)
	if !ok {
		return nil
	}
	if len(live.FetchedModels) > 0 && (live.FetchedKey == "" || live.FetchedKey == key) {
		live.FetchedKey = key
		return live.FetchedModels
	}
	live.FetchedModels = nil
	live.FetchedAt = time.Time{}
	live.FetchedKey = key
	ids, fresh := provider.CachedModelCatalog(key)
	if !fresh {
		return nil
	}
	live.FetchedModels = ids
	live.FetchedAt = time.Now()
	return ids
}

// fetchModelCatalogIDs queries the live provider's endpoint for its real model
// catalog (issue #566), persists it to the on-disk cache, caches the ids on
// live, and returns them. It backs the bare-/model list and numeric selection.
// When the endpoint fails but a stale cache entry exists, the stale list is
// served instead of an error: a day-old model list beats a broken picker.
func fetchModelCatalogIDs(live *cli.LiveConfig, creds *provider.CredentialStore) ([]string, error) {
	if creds == nil {
		return nil, fmt.Errorf("no credential store")
	}
	spec, ok := provider.LookupProviderSpec(live.ProviderName)
	if !ok {
		return nil, fmt.Errorf("unknown provider %q", live.ProviderName)
	}
	baseURL := provider.ResolveBaseURL(spec, live.BaseURL, os.Getenv)
	protocol := live.Protocol
	if strings.TrimSpace(protocol) == "" {
		protocol = spec.Protocol
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ids, err := provider.FetchProviderModels(ctx, live.ProviderName, baseURL, protocol, creds.GetAPIKey(ctx, live.ProviderName))
	if err != nil {
		if key, ok := modelCatalogKey(live); ok {
			if stale, _ := provider.CachedModelCatalog(key); len(stale) > 0 {
				live.FetchedModels = stale
				live.FetchedAt = time.Now()
				live.FetchedKey = key
				return stale, nil
			}
		}
		if provider.NeedsProxy(live.ProviderName, baseURL) && provider.ProxyURL() == "" {
			return nil, fmt.Errorf("%w (provider %q usually needs egress: export GOLDER_PROXY=http://127.0.0.1:7897 and retry)", err, live.ProviderName)
		}
		return nil, err
	}
	if key, ok := modelCatalogKey(live); ok {
		provider.StoreModelCatalog(key, ids)
		live.FetchedKey = key
	}
	live.FetchedModels = ids
	live.FetchedAt = time.Now()
	return ids, nil
}

// EnsureModelCatalog returns the live catalog, preferring the session cache,
// then a fresh disk cache, and only then the network.
func EnsureModelCatalog(live *cli.LiveConfig, creds *provider.CredentialStore) ([]string, error) {
	if ids := CachedModelCatalog(live); len(ids) > 0 {
		return ids, nil
	}
	return fetchModelCatalogIDs(live, creds)
}

func formatNotifications(notes []plugin.CommandNotification) string {
	if len(notes) == 0 {
		return ""
	}
	var b strings.Builder
	for i, n := range notes {
		if i > 0 {
			b.WriteString("\n")
		}
		if n.Type != "" {
			b.WriteString("[")
			b.WriteString(n.Type)
			b.WriteString("] ")
		}
		b.WriteString(n.Message)
	}
	return b.String()
}

// RegisterLiveCommands installs the built-in action commands that need live
// runtime state. /model views or switches the active model; /help lists the
// available commands. These are instance built-ins (AddBuiltin) because their
// closures must capture live and the registry — state unreachable from an
// init()-time global registration. creds resolves the API key for "/model"
// live catalog discovery; it may be nil, which disables fetching (bare /model
// then reports the failure and suggests /model <id>).
// thinkDisplay renders the live reasoning-effort level for status lines,
// matching thinkAction's empty convention.
func thinkDisplay(live *cli.LiveConfig) agentcore.ThinkingLevel {
	if live.ThinkingLevel == "" {
		return agentcore.ThinkingOff
	}
	return live.ThinkingLevel
}

// CredentialSummary reports a provider's credential availability as a short,
// value-free label for listings and pickers: "set", "set via config file",
// "set via --api-key", "set via oauth", or "not set". It is a presence probe
// that never names which variable satisfied it — the listing already shows
// the candidate variables in their own column, so repeating one here is noise.
// creds may be nil, which probes the environment layer only.
func CredentialSummary(creds *provider.CredentialStore, name string) string {
	st := credentialStatus(creds, name)
	if !st.Available() {
		return "not set"
	}
	if st.EnvVar != "" {
		return "set"
	}
	return "set via " + st.Source()
}

// credentialStatus probes a provider's credential layers. creds may be nil, in
// which case only the environment layer is probed.
func credentialStatus(creds *provider.CredentialStore, name string) provider.CredentialStatus {
	if creds != nil {
		return creds.Status(name)
	}
	var st provider.CredentialStatus
	if envVar, ok := provider.EnvCredentialVar(name); ok {
		st.EnvVar = envVar
	}
	return st
}

// CredentialAvailable reports whether any credential layer can supply a key
// for a provider. It is a presence probe: no value is read or returned.
func CredentialAvailable(creds *provider.CredentialStore, name string) bool {
	return credentialStatus(creds, name).Available()
}

// ProvidersAvailableFirst returns the built-in provider specs ordered for
// display: providers with a credential found first (so the ready-to-use ones
// are quick to pick), then the rest. Both groups keep registry order.
func ProvidersAvailableFirst(creds *provider.CredentialStore) []provider.ProviderSpec {
	specs := provider.ProviderSpecs()
	ready := make([]provider.ProviderSpec, 0, len(specs))
	rest := make([]provider.ProviderSpec, 0, len(specs))
	for _, spec := range specs {
		if CredentialAvailable(creds, spec.Name) {
			ready = append(ready, spec)
		} else {
			rest = append(rest, spec)
		}
	}
	return append(ready, rest...)
}

// protocolSuffix renders ", protocol: <effective wire>" for a resolved
// provider/model pair, or "" when it cannot be determined. Switch
// confirmations carry it so a mid-session protocol change is visible in
// history even though the startup banner stays a launch-time snapshot
// (banner = snapshot, status bar = live state, changes = appended lines).
func protocolSuffix(providerName, model, protocol string) string {
	eff := provider.EffectiveProtocol(providerName, model, protocol)
	if eff == "" {
		return ""
	}
	return ", protocol: " + provider.ProtocolLabel(eff)
}

func RegisterLiveCommands(reg *runtime.SlashRegistry, live *cli.LiveConfig, creds *provider.CredentialStore) {
	registerProxyCommand(reg)
	// thinkAction views or switches the reasoning-effort level. It backs both
	// /think and its alias /effect, so the two commands share identical behavior.
	thinkAction := func(args string) string {
		lvl := strings.TrimSpace(args)
		if lvl == "" {
			cur := live.ThinkingLevel
			if cur == "" {
				cur = agentcore.ThinkingOff
			}
			msg := fmt.Sprintf("think: %s\nswitch with /think <off|minimal|low|medium|high|xhigh|max>", cur)
			if lv := provider.KnownReasoningLevels(live.ProviderName, live.Model); len(lv) > 0 {
				msg += fmt.Sprintf("\n%s lists: %s", live.Model, joinThinkingLevels(lv))
			}
			return msg
		}
		v, ok := validThinkingLevel(lvl)
		if !ok {
			return fmt.Sprintf("think: invalid level %q (want off|minimal|low|medium|high|xhigh|max)", lvl)
		}
		live.ThinkingLevel = v
		msg := fmt.Sprintf("think level set to %s (applies to the next turn)", v)
		// The catalog is authoritative about what the model accepts; warn when
		// the request will be clamped on the wire instead of silently differing.
		if v != agentcore.ThinkingOff {
			if lv := provider.KnownReasoningLevels(live.ProviderName, live.Model); len(lv) > 0 && !slices.Contains(lv, v) {
				msg += fmt.Sprintf("\nnote: %s lists %s; the request clamps into that list", live.Model, joinThinkingLevels(lv))
			}
		}
		return msg
	}
	modelSwitch := func(id string) (string, bool) {
		// An id from the fetched online catalog (issue #566) stays on the
		// gateway that served it: resolve with the live provider name
		// explicit instead of the heuristic chain, which could route a
		// gateway-specific id to OpenRouter.
		if providerName := live.ProviderName; len(live.FetchedModels) > 0 && slices.Contains(live.FetchedModels, id) {
			prov, name, err := provider.ResolveProvider(id, live.BaseURL, live.Protocol, providerName, os.Getenv)
			if err != nil {
				return fmt.Sprintf("model: cannot switch to %q: %v", id, err), false
			}
			live.Model = id
			live.ProviderName = name
			live.Provider = prov
			return fmt.Sprintf("model switched to %s (provider: %s%s, from fetched catalog)", id, name, protocolSuffix(name, id, live.Protocol)), true
		}
		model := provider.CanonicalizeModel(id)
		// Fork: stay on the current provider. A bare provider name
		// ("zai", issue #564) keeps the old jump: its canonical id
		// differs from the input, so the provider is left to resolve.
		providerName := live.ProviderName
		if model != id {
			providerName = ""
		}
		prov, providerName, err := provider.ResolveProvider(model, live.BaseURL, live.Protocol, providerName, os.Getenv)
		if err != nil {
			return fmt.Sprintf("model: cannot switch to %q: %v", id, err), false
		}
		live.Model = model
		live.ProviderName = providerName
		live.Provider = prov
		return fmt.Sprintf("model switched to %s (provider: %s%s)", model, providerName, protocolSuffix(providerName, model, live.Protocol)), true
	}
	// reasoningHint is a one-line pointer to the model's catalog reasoning
	// levels, appended after a plain switch so the level step is discoverable.
	// Empty when models.dev has nothing definite for the model.
	reasoningHint := func(model string) string {
		lv := provider.KnownReasoningLevels(live.ProviderName, model)
		if len(lv) == 0 {
			return ""
		}
		return fmt.Sprintf("\nreasoning levels: %s (/think <level> or /model %s <level>)", joinThinkingLevels(lv), model)
	}
	// pickModel resolves a selector — a numeric index into the live catalog or a
	// model id — and switches to it. Both the single-argument form and the
	// model+level form share it, so "/model 2" and "/model 2 high" agree.
	pickModel := func(sel string) (string, bool) {
		if n, err := strconv.Atoi(sel); err == nil {
			ids, err := EnsureModelCatalog(live, creds)
			if err != nil {
				return fmt.Sprintf("model: list unavailable: %v\nswitch directly with /model <id>", err), false
			}
			if n < 1 || n > len(ids) {
				return fmt.Sprintf("model: %d out of range (1-%d)", n, len(ids)), false
			}
			return modelSwitch(ids[n-1])
		}
		return modelSwitch(sel)
	}
	providerAction := func(args string) string {
		arg := strings.TrimSpace(args)
		if arg == "" {
			specs := provider.ProviderSpecs()
			// Dynamic column width so a long env list (or long provider name)
			// does not push the credential column out of alignment.
			envWidth := len("no key needed")
			nameWidth := len("provider")
			for _, spec := range specs {
				if w := len(spec.Name); w > nameWidth {
					nameWidth = w
				}
				w := len("no key needed")
				if len(spec.EnvVars) > 0 {
					w = len(strings.Join(spec.EnvVars, " / "))
				}
				if w > envWidth {
					envWidth = w
				}
			}
			row := func(spec provider.ProviderSpec) string {
				envs := "no key needed"
				if len(spec.EnvVars) > 0 {
					envs = strings.Join(spec.EnvVars, " / ")
				}
				line := fmt.Sprintf("  %-*s  %-*s  %s", nameWidth, spec.Name, envWidth, envs, CredentialSummary(creds, spec.Name))
				if spec.Name == live.ProviderName {
					line += "  (current)"
				}
				line += "\n    " + ProviderEndpointSummary(live, spec)
				return line
			}
			var ready, missing []provider.ProviderSpec
			for _, spec := range specs {
				if CredentialAvailable(creds, spec.Name) {
					ready = append(ready, spec)
				} else {
					missing = append(missing, spec)
				}
			}
			var b strings.Builder
			fmt.Fprintf(&b, "provider: %s\nswitch with /provider <name>; set any one of the listed variables (earlier ones win)", live.ProviderName)
			if len(ready) == 0 {
				b.WriteString("\n\nno provider credentials found on this machine; set one of the variables below")
			} else {
				b.WriteString("\n\nready to use (credential found):")
				for _, spec := range ready {
					b.WriteString("\n" + row(spec))
				}
			}
			if len(missing) > 0 {
				b.WriteString("\n\nneeds a key (set one of these):")
				for _, spec := range missing {
					b.WriteString("\n" + row(spec))
				}
			}
			b.WriteString("\n\nBASE_URL is optional and applies to both chat and /models. Configure routing with /proxy.")
			b.WriteString("\nlocal: ollama needs no key (use /model ollama/<model>)")
			return b.String()
		}
		spec, ok := provider.LookupProviderSpec(arg)
		if !ok {
			return fmt.Sprintf("provider: unknown provider %q\nsupported: %s", arg, strings.Join(provider.ProviderNames(), ", "))
		}
		model := live.Model
		note := ""
		if spec.DefaultModel != "" {
			model = spec.DefaultModel
		} else {
			note = "\nno default model for this provider; set one with /model <id>"
		}
		// A provider switch targets the new provider's registry endpoint: a
		// --base-url / --protocol override belongs to the previous provider and
		// would otherwise leak into the new driver (mirrors session switching).
		cleared := live.BaseURL != "" || live.Protocol != ""
		live.BaseURL = ""
		live.Protocol = ""
		prov, name, err := provider.ResolveProvider(model, live.BaseURL, live.Protocol, spec.Name, os.Getenv)
		if err != nil {
			return fmt.Sprintf("provider: cannot switch to %q: %v", arg, err)
		}
		live.Model = model
		live.ProviderName = name
		live.Provider = prov
		// The fetched catalog belongs to the previous gateway; drop it so the
		// next bare /model lists (and disk-caches) the new provider's lineup.
		live.FetchedModels = nil
		live.FetchedAt = time.Time{}
		live.FetchedKey = ""
		msg := fmt.Sprintf("provider switched to %s (model: %s%s)", name, model, protocolSuffix(name, model, live.Protocol))
		if cleared {
			msg += "\n(base-url/protocol override cleared; using the provider's environment/default endpoint)"
		}
		msg += "\n" + ProviderEndpointSummary(live, spec)
		if !CredentialAvailable(creds, name) && len(spec.EnvVars) > 0 {
			msg += fmt.Sprintf("\nwarning: no credential found; set any one of %s (see /provider for the full list)", strings.Join(spec.EnvVars, " / "))
		}
		return msg + note
	}
	reg.AddBuiltin(runtime.SlashCommand{
		Name:         "model",
		Description:  "list and switch models on the current provider: /model [n|id] [level] | /model think [level]",
		ArgumentHint: "[n|id [level]|think level]",
		Action: func(args string) string {
			arg := strings.TrimSpace(args)
			// /model think [level]: show or set the reasoning effort.
			if arg == "think" || strings.HasPrefix(arg, "think ") {
				return thinkAction(strings.TrimSpace(strings.TrimPrefix(arg, "think")))
			}
			// Bare /model: live catalog as a numbered list.
			if arg == "" {
				ids, err := EnsureModelCatalog(live, creds)
				if err != nil {
					return fmt.Sprintf("model: %s (provider: %s, think: %s)\nmodel list unavailable: %v\nswitch directly with /model <id>",
						live.Model, live.ProviderName, thinkDisplay(live), err)
				}
				var b strings.Builder
				fmt.Fprintf(&b, "model: %s (provider: %s, think: %s)\nmodels on %s (/model <n|id> to switch):",
					live.Model, live.ProviderName, thinkDisplay(live), live.ProviderName)
				for i, id := range ids {
					mark := ""
					if id == live.Model {
						mark = " (current)"
					}
					note := ""
					if lv := provider.KnownReasoningLevels(live.ProviderName, id); len(lv) > 0 {
						note = "  [" + joinThinkingLevels(lv) + "]"
					}
					fmt.Fprintf(&b, "\n  %d. %s%s%s", i+1, id, note, mark)
				}
				return b.String()
			}
			// /model <n|id> <level>: switch and set the reasoning effort in one
			// step (the TUI picker's stage-2 confirm runs this form).
			fields := strings.Fields(arg)
			if len(fields) == 2 {
				lvl, ok := validThinkingLevel(fields[1])
				if !ok {
					return fmt.Sprintf("model: invalid reasoning level %q (want off|minimal|low|medium|high|xhigh|max)", fields[1])
				}
				msg, switched := pickModel(fields[0])
				if !switched {
					return msg
				}
				live.ThinkingLevel = lvl
				return fmt.Sprintf("%s\nthink level set to %s", msg, lvl)
			}
			// /model <n|id>: switch; a catalog-known model also hints its levels.
			msg, switched := pickModel(arg)
			if !switched {
				return msg
			}
			return msg + reasoningHint(live.Model)
		},
	})
	reg.AddBuiltin(runtime.SlashCommand{
		Name:         "think",
		ArgumentHint: "[off|minimal|low|medium|high|xhigh|max]",
		Description:  "view or switch the reasoning-effort level; takes effect on the next turn",
		Action:       thinkAction,
	})
	reg.AddBuiltin(runtime.SlashCommand{
		Name:         "provider",
		Description:  "list providers with credential availability, or switch: /provider [name]",
		ArgumentHint: "[name]",
		Action:       providerAction,
	})
	reg.AddBuiltin(runtime.SlashCommand{
		Name:         "effect",
		ArgumentHint: "[off|minimal|low|medium|high|xhigh|max]",
		Description:  "alias of /think: view or switch the reasoning-effort level",
		Action:       thinkAction,
	})
	reg.AddBuiltin(runtime.SlashCommand{
		Name:        "help",
		Description: "list available slash commands",
		Action: func(string) string {
			color := ui.Enabled()
			var b strings.Builder
			b.WriteString(ui.Colorize(color, ui.Bold, "available commands:"))
			for _, c := range reg.List() {
				b.WriteString("\n  ")
				b.WriteString(ui.Colorize(color, ui.Cyan, "/"+c.Name))
				rest := ""
				if c.ArgumentHint != "" {
					rest += " " + c.ArgumentHint
				}
				if c.Description != "" {
					rest += " - " + c.Description
				}
				rest += " (source: " + c.Tier.String() + ")"
				b.WriteString(ui.Colorize(color, ui.Dim, rest))
			}
			return b.String()
		},
	})
	// /exit, /quit, /compact, /fork, /clone, /tree, /export, /import, /copy,
	// /session and /status are intercepted by the REPL loop before slash resolution
	// (they must return from the loop, run an agent stream, or read/swap the active
	// session/leaf — none of which an Action closure can do). They are registered
	// here only so /help lists them; their Action is never actually reached.
	for _, c := range []struct{ name, desc string }{
		{"exit", "exit the REPL"},
		{"quit", "exit the REPL"},
		{"compact", "summarize and compact the conversation context now"},
		{"fork", "branch from a historical message into a new session: /fork [n]"},
		{"clone", "duplicate the current session into an independent branch"},
		{"tree", "show the session branch tree; switch active branch: /tree [n]"},
		{"rewind", "roll files and the conversation back to before an earlier turn: /rewind [n]"},
		{"export", "export the session to a file: /export [path.jsonl|path.html]"},
		{"import", "import a JSONL export as a new session: /import <path.jsonl>"},
		{"copy", "copy the most recent assistant reply to the clipboard"},
		{"session", "show session stats: messages, tokens, model, compactions"},
		{"resume", "switch session: /resume [n|id] (bare lists recent sessions)"},
		{"status", "show session status: runtime config, context, telemetry, credentials, environment"},
		{"goal", "run autonomously toward a goal: /goal [--tokens N] <objective> | pause | resume | clear"},
		{"btw", "ask a quick side question without touching the main conversation: /btw <question> (bare /btw reopens the last one)"},
		{"dream", "consolidate memory now (dedupe, merge, prune, distill); /dream --dry-run previews without writing"},
		{"remote-control", "mirror this session to a phone/browser on your LAN: /remote-control [stop|status]"},
	} {
		reg.AddBuiltin(runtime.SlashCommand{
			Name:        c.name,
			Description: c.desc,
			Action:      func(string) string { return "" },
		})
	}
}

// validThinkingLevel reports whether s is one of the known reasoning-effort
// levels and returns the typed value. It mirrors the enum in agentcore so a
// /think argument can be validated without importing the config layer.
func validThinkingLevel(s string) (agentcore.ThinkingLevel, bool) {
	switch agentcore.ThinkingLevel(s) {
	case agentcore.ThinkingOff, agentcore.ThinkingMinimal, agentcore.ThinkingLow,
		agentcore.ThinkingMedium, agentcore.ThinkingHigh, agentcore.ThinkingXHigh, agentcore.ThinkingMax:
		return agentcore.ThinkingLevel(s), true
	default:
		return "", false
	}
}

// joinThinkingLevels renders a level list as "low|medium|high" for status text.
func joinThinkingLevels(levels []agentcore.ThinkingLevel) string {
	parts := make([]string, len(levels))
	for i, l := range levels {
		parts[i] = string(l)
	}
	return strings.Join(parts, "|")
}

// RegisterPermissionCommand installs the /permissions action command: with an
// argument it switches the live approval mode (read-only / ask / auto /
// full-access); with none it lists the modes and marks the current one. The
// closure captures the shared state object, so the switch applies to the next
// tool call in both the plain REPL and the TUI. lang reports the language of
// the recent conversation ("zh"/"en") so the output matches what the user is
// writing; a nil lang defaults to English.
func RegisterPermissionCommand(reg *runtime.SlashRegistry, st *permissions.State, lang func() string) {
	if reg == nil {
		return
	}
	pickLang := func() string {
		if lang != nil && lang() == "zh" {
			return "zh"
		}
		return "en"
	}
	modes := []permissions.Mode{permissions.ReadOnly, permissions.Ask, permissions.Auto, permissions.FullAccess}
	reg.AddBuiltin(runtime.SlashCommand{
		Name:         "permissions",
		Description:  "show or switch the approval mode: /permissions [read-only|ask|auto|full-access]",
		ArgumentHint: "[read-only|ask|auto|full-access]",
		Action: func(args string) string {
			l := pickLang()
			arg := strings.TrimSpace(args)
			if arg == "" {
				current := permissions.Auto
				if st != nil {
					current = st.Mode()
				}
				var b strings.Builder
				if l == "zh" {
					fmt.Fprintf(&b, "当前权限模式：%s（%s）\n可选模式（/permissions <模式> 切换）：",
						current.Label(l), current.Description(l))
				} else {
					fmt.Fprintf(&b, "permissions: %s (%s)\nmodes (/permissions <mode> to switch):",
						current.Label(l), current.Description(l))
				}
				for _, m := range modes {
					mark := ""
					if m == current {
						if l == "zh" {
							mark = "（当前）"
						} else {
							mark = " (current)"
						}
					}
					fmt.Fprintf(&b, "\n  %-12s %s%s", m.String(), m.Label(l), mark)
				}
				return b.String()
			}
			m, ok := permissions.Parse(arg)
			if !ok {
				if l == "zh" {
					return "用法：/permissions [read-only|ask|auto|full-access]"
				}
				return "usage: /permissions [read-only|ask|auto|full-access]"
			}
			if st != nil {
				st.Set(m)
			}
			if l == "zh" {
				return fmt.Sprintf("权限模式已切换为 %s：%s", m.Label(l), m.Description(l))
			}
			return fmt.Sprintf("permissions switched to %s: %s", m.Label(l), m.Description(l))
		},
	})
}
