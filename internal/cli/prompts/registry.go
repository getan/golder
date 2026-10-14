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
	"github.com/getan/golder/internal/golderhome"
	"github.com/getan/golder/internal/grants"
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
		dir := golderhome.Dir()
		if dir == "" {
			return reg, nil // built-ins only
		}
		// Load user prompt templates from both the compat commands/ directory and
		// the pi-aligned prompts/ one (both non-recursive, global tier).
		// Loading commands first means a same-named template in prompts/
		// overrides the legacy one (last-write-wins within the global tier). A
		// missing directory is not an error (LoadUserCommandsDir returns nil,
		// nil for IsNotExist).
		for _, sub := range []string{golderhome.CommandsDir, golderhome.PromptsDir} {
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
		// A decision-only endpoint is not a conversation model: offering it in a
		// provider picker offers a switch that cannot work (see
		// ProviderSpec.DecisionOnly). /provider still lists it, in its own
		// section, so its credential and proxy routing stay discoverable.
		if spec.DecisionOnly {
			continue
		}
		if CredentialAvailable(creds, spec.Name) {
			ready = append(ready, spec)
		} else {
			rest = append(rest, spec)
		}
	}
	return append(ready, rest...)
}

// DecisionModelSpecs returns the registry's decision-only endpoints, in registry
// order. They are not conversation providers, so no provider LIST carries them;
// a screen that explains the setup still names them, because their key and their
// proxy route are things the user configures. Callers show them in a section of
// their own — the /provider listing and the TUI picker both do.
func DecisionModelSpecs() []provider.ProviderSpec {
	var out []provider.ProviderSpec
	for _, spec := range provider.ProviderSpecs() {
		if spec.DecisionOnly {
			out = append(out, spec)
		}
	}
	return out
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

// contextSuffix names the model's catalog context window in switch
// confirmations, so the status bar's percentage has a visible denominator
// right where the model changes. Empty when the catalog does not know it.
func contextSuffix(providerName, model string) string {
	if w := provider.ContextWindowFor(providerName, model); w > 0 {
		return ", context: " + cli.FormatContextWindow(w)
	}
	return ""
}

func RegisterLiveCommands(reg *runtime.SlashRegistry, live *cli.LiveConfig, creds *provider.CredentialStore) {
	registerProxyCommand(reg)
	// thinkAction views or switches the reasoning-effort level, backing the
	// /model think [level] form. It used to also back a standalone /think; that
	// command was retired because the level belongs to the model and /model
	// already carries both (see runtime.retiredCommands for the redirect a
	// lingering /think gets).
	thinkAction := func(args string) string {
		lvl := strings.TrimSpace(args)
		if lvl == "" {
			cur := live.ThinkingLevel
			if cur == "" {
				cur = agentcore.ThinkingOff
			}
			msg := fmt.Sprintf("think: %s\nswitch with /model think <off|minimal|low|medium|high|xhigh|max>", cur)
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
			// Re-resolve the budget for the new model: the status bar and the
			// auto-compaction threshold read it from the live config.
			live.RefreshContextWindow()
			return fmt.Sprintf("model switched to %s (provider: %s%s%s, from fetched catalog)",
				id, name, protocolSuffix(name, id, live.Protocol), contextSuffix(name, id)), true
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
		live.RefreshContextWindow()
		return fmt.Sprintf("model switched to %s (provider: %s%s%s)",
			model, providerName, protocolSuffix(providerName, model, live.Protocol), contextSuffix(providerName, model)), true
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
			var decision []provider.ProviderSpec
			for _, spec := range specs {
				switch {
				case spec.DecisionOnly:
					decision = append(decision, spec)
				case CredentialAvailable(creds, spec.Name):
					ready = append(ready, spec)
				default:
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
			if len(decision) > 0 {
				// Listed apart, because it answers a different question: this
				// is the endpoint the reviewer asks, not a model you can talk
				// to. Keeping it in the chat list made it look switchable.
				b.WriteString("\n\ndecision model (answers the reviewer; not a conversation provider):")
				for _, spec := range decision {
					b.WriteString("\n" + row(spec))
				}
				b.WriteString("\n    route its requests with /proxy; the conversation model is chosen above")
			}
			b.WriteString("\n\nBASE_URL is optional and applies to both chat and /models. Configure routing with /proxy.")
			b.WriteString("\nlocal: ollama needs no key (use /model ollama/<model>)")
			return b.String()
		}
		spec, ok := provider.LookupProviderSpec(arg)
		if !ok {
			return fmt.Sprintf("provider: unknown provider %q\nsupported: %s", arg, strings.Join(provider.ProviderNames(), ", "))
		}
		if spec.DecisionOnly {
			return fmt.Sprintf("provider: %s is a decision model, not a conversation provider — it answers the reviewer's typed questions, so switching to it would break every turn.\nRoute its requests with /proxy %s on; the conversation model stays %s.",
				spec.Name, spec.Name, live.ProviderName)
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
		live.RefreshContextWindow()
		// The fetched catalog belongs to the previous gateway; drop it so the
		// next bare /model lists (and disk-caches) the new provider's lineup.
		live.FetchedModels = nil
		live.FetchedAt = time.Time{}
		live.FetchedKey = ""
		msg := fmt.Sprintf("provider switched to %s (model: %s%s%s)",
			name, model, protocolSuffix(name, model, live.Protocol), contextSuffix(name, model))
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
		Category:     runtime.CategoryModel,
		Description:  "list and switch models on the current provider",
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
				fmt.Fprintf(&b, "model: %s (provider: %s, think: %s%s)\nmodels on %s (/model <n|id> to switch):",
					live.Model, live.ProviderName, thinkDisplay(live),
					contextSuffix(live.ProviderName, live.Model), live.ProviderName)
				for i, id := range ids {
					mark := ""
					if id == live.Model {
						mark = " (current)"
					}
					note := ""
					// The catalog window leads each row it knows, before the
					// reasoning bracket: the number is the scan target (how
					// much room does this model have?), the levels are detail.
					if w := provider.ContextWindowFor(live.ProviderName, id); w > 0 {
						note = "  " + cli.FormatContextWindow(w)
					}
					if lv := provider.KnownReasoningLevels(live.ProviderName, id); len(lv) > 0 {
						note += "  [" + joinThinkingLevels(lv) + "]"
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
		Name:         "provider",
		Category:     runtime.CategoryModel,
		Description:  "list providers with credential availability, or switch",
		ArgumentHint: "[name]",
		Action:       providerAction,
	})
	reg.AddBuiltin(runtime.SlashCommand{
		Name:        "help",
		Category:    runtime.CategoryGeneral,
		Description: "list commands by category",
		Action:      func(string) string { return renderHelp(reg, runtime.CategoryOrder) },
	})
	// These commands are intercepted by the REPL loop and the TUI before slash
	// resolution (they must return from the loop, run an agent stream, or
	// read/swap the active session/leaf — none of which a string→string Action
	// closure can do). They are registered here only so /help lists them; the
	// empty Action is a placeholder and both front-ends intercept them first.
	//
	// name/hint/description are three fields because they are three different
	// things to three different readers (see the builtinUsage convention test):
	// the hint is the signature the user types, the description is the sentence
	// they read, and a description that had to carry usage was the shape that made
	// /permissions print a hand-written "…" as if it were a renderer's ellipsis.
	// An empty hint means the command takes no arguments.
	for _, c := range []struct{ name, hint, desc, category string }{
		{"exit", "", "exit golder", runtime.CategoryGeneral},
		{"compact", "", "compact the conversation context now", runtime.CategorySession},
		{"fork", "[n]", "branch from a historical message into a new session", runtime.CategorySession},
		{"clone", "", "duplicate the current session into an independent branch", runtime.CategorySession},
		{"tree", "[n]", "show the session branch tree, or switch the active branch", runtime.CategorySession},
		{"rewind", "[n]", "roll files and the conversation back to before an earlier turn", runtime.CategorySession},
		{"export", "[path.jsonl|path.html]", "export the session to a file", runtime.CategorySession},
		{"import", "<path.jsonl>", "import a JSONL export as a new session", runtime.CategorySession},
		{"resume", "[--all] [n|id]", "switch session (bare, it lists this project's recent sessions)", runtime.CategorySession},
		{"status", "", "show session status (session, runtime config, context, credentials, environment)", runtime.CategoryGeneral},
		{"memory", "", "show the persistent-memory report", runtime.CategoryMemory},
		{"dream", "[--dry-run]", "consolidate memory now (dedupe, merge, prune, distill)", runtime.CategoryMemory},
		{"goal", "[--tokens N] <objective> | pause | resume | clear", "run autonomously toward a goal", runtime.CategoryModes},
		{"btw", "[question]", "ask a quick side question without touching the main conversation (bare, it reopens the last one)", runtime.CategoryModes},
		{"remote-control", "[stop|status]", "mirror this session to a phone/browser on your LAN", runtime.CategoryModes},
	} {
		reg.AddBuiltin(runtime.SlashCommand{
			Name:         c.name,
			ArgumentHint: c.hint,
			Description:  c.desc,
			Category:     c.category,
			Action:       func(string) string { return "" },
		})
	}
}

// renderHelp renders the /help listing grouped by category: built-ins under
// their own category, skills/plugins/prompt templates collected under
// Extensions with their source tag. order is the category render order.
func renderHelp(reg *runtime.SlashRegistry, order []string) string {
	color := ui.Enabled()
	groups := make(map[string][]runtime.SlashCommand)
	for _, c := range reg.List() {
		cat := runtime.CategoryOf(c)
		groups[cat] = append(groups[cat], c)
	}
	var b strings.Builder
	b.WriteString(ui.Colorize(color, ui.Bold, "available commands:"))
	for _, cat := range order {
		cmds := groups[cat]
		if len(cmds) == 0 {
			continue
		}
		b.WriteString("\n\n")
		b.WriteString(ui.Colorize(color, ui.Bold, cat))
		for _, c := range cmds {
			rest := ""
			if c.ArgumentHint != "" {
				rest += " " + c.ArgumentHint
			}
			if c.Description != "" {
				rest += "  " + c.Description
			}
			if cat == runtime.CategoryExtensions {
				rest += " (source: " + extensionSource(c) + ")"
			}
			b.WriteString("\n")
			b.WriteString(ui.Colorize(color, ui.Cyan, "  /"+c.Name))
			b.WriteString(ui.Colorize(color, ui.Dim, rest))
		}
	}
	return b.String()
}

// extensionSource labels an extension command: skills and plugins by their
// kind, prompt templates by their discovery tier (global/project/settings/cli).
func extensionSource(c runtime.SlashCommand) string {
	switch c.Source {
	case runtime.SourceSkill:
		return "skill"
	case runtime.SourcePlugin:
		return "plugin"
	default:
		return c.Tier.String()
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

// The /permissions grammar, in the two shapes the command needs. The whole-command
// form is the registry hint (what the popup and /help advertise); the two
// subcommand forms are what a typo *inside* a subcommand is answered with, spelled
// out because they are narrower — `list` takes no path where `add`/`rm` require
// one, and a reader who typed the verb wants that difference, not the whole
// command's bracket soup. They are declared together so a new verb is added where
// the hint is read as well as where the parser is.
const (
	permissionsArgumentHint = "[list | read-only|ask|auto|full-access | writable " +
		permissionsWritableHint + " | readable " + permissionsReadableHint + "]"
	// The subcommand slices as the hint spells them (compact) and as the subcommand
	// usage spells them (spelled out).
	permissionsWritableHint = "[list|add|rm] <path>"
	permissionsReadableHint = "[list|add|rm] <path>"

	permissionsWritableUsage = "[list | add <path> | rm <path>]"
	permissionsReadableUsage = "[list | add <path> | rm <path>]"
)

// usageLine renders the line a command answers a bad argument with, from the
// registry's own record of that command: the grammar the user is being taught is
// the hint the popup and /help advertise, because they are the same string. The
// Chinese spelling swaps the "usage: " prefix only — the grammar itself is not
// translated, since the words in it are the literals the user types.
func usageLine(reg *runtime.SlashRegistry, name, lang string) string {
	line := reg.UsageLine(name)
	if lang == "zh" {
		return "用法：" + strings.TrimPrefix(line, "usage: ")
	}
	return line
}

// usageLineFor renders a usage line for a grammar this package declares but the
// registry does not carry on its own — a subcommand's narrower form (see the
// permissions constants). It exists so the English and Chinese spellings of one
// grammar are written once, next to that grammar, instead of twice inside the
// function that answers the error.
func usageLineFor(name, grammar string, zh bool) string {
	if zh {
		return "用法：/" + name + " " + grammar
	}
	return "usage: /" + name + " " + grammar
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
		Category:     runtime.CategoryPermissions,
		Description:  "show or switch the approval mode",
		ArgumentHint: "[list | read-only|ask|auto|full-access]",
		Action: func(args string) string {
			l := pickLang()
			arg := strings.TrimSpace(args)
			if arg == "list" {
				return runPermissionsList(st, modes, l)
			}
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
				if l == "zh" {
					fmt.Fprintf(&b, "\n路径授权：/boundary list　全部条目：/grants")
				} else {
					fmt.Fprintf(&b, "\npath grants: /boundary list  every row: /grants")
				}
				return b.String()
			}
			m, ok := permissions.Parse(arg)
			if !ok {
				// Composed from the registry's own hint, so the line the user gets
				// after a typo is the grammar the popup and /help advertise. Typed
				// twice, these drifted: the hint learned `list` and the error line
				// did not.
				return usageLine(reg, "permissions", l)
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

// RegisterBoundaryCommand installs /boundary: which paths the read and write
// tools may already reach without asking.
//
// These grants used to hang off /permissions as "writable" and "readable"
// subcommands, which put two different subjects on one command. The workspace
// boundary is consent — where data may come from, and where it may go — while
// the approval mode is about how calls are graded; a person auditing one is not
// auditing the other. The three doors now each speak for themselves: /trust for
// the directory, /boundary for the paths, /permissions for the mode. /grants
// stays the escape hatch that shows and edits every row of the one table.
//
// lang reports the conversation language ("zh"/"en") so the output matches what
// the user is writing; a nil lang defaults to English.
func RegisterBoundaryCommand(reg *runtime.SlashRegistry, lang func() string) {
	if reg == nil {
		return
	}
	pickLang := func() string {
		if lang != nil && lang() == "zh" {
			return "zh"
		}
		return "en"
	}
	reg.AddBuiltin(runtime.SlashCommand{
		Name:         "boundary",
		Category:     runtime.CategoryPermissions,
		Description:  "allow reading or writing outside the workspace",
		ArgumentHint: "[list | readable [list|add|rm] <path> | writable [list|add|rm] <path>]",
		Action: func(args string) string {
			l := pickLang()
			arg := strings.TrimSpace(args)
			switch {
			case arg == "writable" || strings.HasPrefix(arg, "writable "):
				return runBoundaryWritable(arg, l)
			case arg == "readable" || strings.HasPrefix(arg, "readable "):
				return runBoundaryReadable(arg, l)
			case arg == "", arg == "list":
				return runBoundaryList(l)
			}
			return usageLine(reg, "boundary", l)
		},
	})
}

// runPermissionsList renders every permission in force, not just the ones that
// apply to the current directory: the approval mode, the sandbox writable roots,
// and the readable roots outside the workspace, each with the file it came from
// (grants.json / this session). It is the
// overview the per-scope subcommands (`writable list`, `readable list`) are
// slices of — one screen to audit before revoking something, and the one place
// that names the files a user can edit by hand.
//
// The bare `/permissions` stays the mode picker (it is what the TUI's mode
// picker prints as text); this is the full report, which the bare form points at.
// grantsPath names the file every grant lives in, for the report's footer. The
// table resolves its own location (DefaultPath honours the environment), so
// asking it is what keeps the line truthful if that ever moves.
func grantsPath() string { return grants.DefaultPath(os.Getenv) }

func runPermissionsList(st *permissions.State, modes []permissions.Mode, l string) string {
	zh := l == "zh"
	current := permissions.Auto
	if st != nil {
		current = st.Mode()
	}
	var b strings.Builder
	if zh {
		fmt.Fprintf(&b, "当前权限模式：%s（%s）", current.Label(l), current.Description(l))
	} else {
		fmt.Fprintf(&b, "approval mode: %s (%s)", current.Label(l), current.Description(l))
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "%s %s", permissionModeListLabel(zh), permissionModesInline(modes))

	// Paths are deliberately absent: they are the boundary gate's business, and
	// the two commands stay one subject each. /boundary lists them, /grants
	// shows every row with its provenance.
	if zh {
		b.WriteString("\n切换：/permissions <模式>　路径授权：/boundary list")
	} else {
		b.WriteString("\nswitch: /permissions <mode>  path grants: /boundary list")
	}
	return b.String()
}

// runBoundaryList renders the two kinds of path grant the boundary gate owns:
// what the read and write tools may already reach without asking.
//
// It is a separate command from /permissions on purpose. The workspace boundary
// is consent — where data may come from, and where it may go — while the
// approval mode is about how calls are graded; a person auditing one is not
// auditing the other, and putting them on one screen taught people to look for
// "permissions" when they meant "where may this read".
func runBoundaryList(l string) string {
	zh := l == "zh"
	writable, warnW := cli.ListWritablePaths()
	readable, warnR := cli.ListReadablePaths()
	var b strings.Builder
	if zh {
		b.WriteString("沙箱可写路径（项目目录与临时目录之外额外授权）：")
	} else {
		b.WriteString("sandbox writable paths (extra grants beyond the project and temp dirs):")
	}
	if len(writable) == 0 {
		b.WriteString(noneWord(zh))
	}
	for _, e := range writable {
		fmt.Fprintf(&b, "\n  %-40s %s", cli.DisplayHomePath(e.Path), writableSourceLabel(e.Source, zh))
	}
	b.WriteString("\n")
	if zh {
		b.WriteString("工作区外的可读路径（读取不再询问）：")
	} else {
		b.WriteString("readable paths outside the workspace (reads no longer ask):")
	}
	if len(readable) == 0 {
		b.WriteString(noneWord(zh))
	}
	for _, e := range readable {
		fmt.Fprintf(&b, "\n  %-40s %s", cli.DisplayHomePath(e.Path), cli.ReadableSourceLabel(e.Source, zh))
	}
	for _, w := range append(append([]string{}, warnW...), warnR...) {
		fmt.Fprintf(&b, "\nwarning: %s", w)
	}
	if zh {
		fmt.Fprintf(&b, "\n文件：%s（唯一授权存储）", cli.DisplayHomePath(grantsPath()))
		fmt.Fprintf(&b, "\n修改：/boundary writable add <路径>　/boundary readable add <路径>")
		b.WriteString("\n全部条目（含信任与拒绝）：/grants")
	} else {
		fmt.Fprintf(&b, "\nfile: %s (the one authorization store)", cli.DisplayHomePath(grantsPath()))
		fmt.Fprintf(&b, "\nchange: /boundary writable add <path>  /boundary readable add <path>")
		b.WriteString("\nevery row (trust and refusals too): /grants")
	}
	return b.String()
}

// permissionModeListLabel names the one-line echo of the available modes, so the
// report says what can be switched to without repeating the switch syntax.
func permissionModeListLabel(zh bool) string {
	if zh {
		return "可选："
	}
	return "modes:"
}

// permissionModesInline renders the modes as one scannable line, marking nothing:
// the current mode is already named above, and a marker on a "modes:" line reads
// as if the list were a picker.
func permissionModesInline(modes []permissions.Mode) string {
	names := make([]string, 0, len(modes))
	for _, m := range modes {
		names = append(names, m.String())
	}
	return strings.Join(names, " | ")
}

// noneWord renders the "nothing here" case, which must not read as a path.
func noneWord(zh bool) string {
	if zh {
		return "无"
	}
	return " none"
}

// writableSourceLabel names where a writable grant came from. It is the same
// vocabulary runPermissionsWritable prints, kept as a function so the two
// surfaces cannot drift into different words for the same provenance.
func writableSourceLabel(source string, zh bool) string {
	switch {
	case source == cli.WritableSourceSaved && zh:
		return "（grants.json，已持久化）"
	case source == cli.WritableSourceSaved:
		return "(grants.json, remembered)"
	case zh:
		return "（仅本会话）"
	default:
		return "(this session)"
	}
}

// runPermissionsWritable implements `/permissions writable [list|add|rm]
// <path>`, the text surface of the shared writable-path management (the TUI
// opens the same data as a picker). arg is the full argument, already known to
// start with "writable".
func runBoundaryWritable(arg, l string) string {
	zh := l == "zh"
	fields := strings.Fields(arg)
	usage := func() string {
		return usageLineFor("boundary writable", permissionsWritableUsage, zh)
	}
	// restAfter returns the path argument after the verb, preserving spaces in
	// the path itself (only the verb token is stripped).
	restAfter := func(verb string) string {
		s := strings.TrimSpace(strings.TrimPrefix(arg, "writable"))
		return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), verb))
	}
	sourceLabel := func(source string) string { return writableSourceLabel(source, zh) }
	if len(fields) == 1 || (len(fields) == 2 && fields[1] == "list") {
		entries, warnings := cli.ListWritablePaths()
		var b strings.Builder
		if zh {
			b.WriteString("沙箱可写路径（项目目录与临时目录之外额外授权）：")
			if len(entries) == 0 {
				b.WriteString("无")
			}
			for _, e := range entries {
				fmt.Fprintf(&b, "\n  %-40s %s", cli.DisplayHomePath(e.Path), sourceLabel(e.Source))
			}
			b.WriteString("\n添加：/boundary writable add <路径>　删除：/boundary writable rm <路径>")
		} else {
			b.WriteString("sandbox writable paths (extra grants beyond the project and temp dirs):")
			if len(entries) == 0 {
				b.WriteString(" none")
			}
			for _, e := range entries {
				fmt.Fprintf(&b, "\n  %-40s %s", cli.DisplayHomePath(e.Path), sourceLabel(e.Source))
			}
			b.WriteString("\nadd: /boundary writable add <path>  remove: /boundary writable rm <path>")
		}
		for _, w := range warnings {
			fmt.Fprintf(&b, "\nwarning: %s", w)
		}
		return b.String()
	}
	if len(fields) >= 2 {
		switch fields[1] {
		case "add":
			p := restAfter("add")
			if p == "" {
				return usage()
			}
			root, err := cli.AddWritablePath(p)
			if err != nil {
				if zh {
					return "无法添加：" + err.Error()
				}
				return "cannot add: " + err.Error()
			}
			display := cli.DisplayHomePath(root)
			if zh {
				return fmt.Sprintf("已授权 %s：沙箱内可读写，已写入 grants.json（重启后仍生效）", display)
			}
			return fmt.Sprintf("granted %s: read+write inside the sandbox, saved to grants.json", display)
		case "rm", "remove":
			p := restAfter(fields[1])
			if p == "" {
				return usage()
			}
			res, err := cli.RemoveWritablePath(p)
			if err != nil {
				if zh {
					return "无法删除：" + err.Error()
				}
				return "cannot remove: " + err.Error()
			}
			display := cli.DisplayHomePath(res.Path)
			if !res.Found() {
				if zh {
					return fmt.Sprintf("未授权该路径：%s", display)
				}
				return fmt.Sprintf("not granted: %s", display)
			}
			var b strings.Builder
			if zh {
				fmt.Fprintf(&b, "已撤销 %s", display)
				if res.WasRemembered {
					b.WriteString("，已从 grants.json 删除")
				}
			} else {
				fmt.Fprintf(&b, "revoked %s", display)
				if res.WasRemembered {
					b.WriteString(", deleted from grants.json")
				}
			}
			return b.String()
		}
	}
	return usage()
}

// runPermissionsReadable is the read-side twin of runPermissionsWritable: the
// same list/add/rm shape over the same managed store, so the two grants are
// maintained the same way. It exists because the read grant had no persisted
// form at all — the approval dialog's answer lived in memory — while the
// writable grant beside it had a command.
func runBoundaryReadable(arg, l string) string {
	zh := l == "zh"
	fields := strings.Fields(arg)
	usage := func() string {
		return usageLineFor("boundary readable", permissionsReadableUsage, zh)
	}
	restAfter := func(verb string) string {
		s := strings.TrimSpace(strings.TrimPrefix(arg, "readable"))
		return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), verb))
	}
	if len(fields) == 1 || (len(fields) == 2 && fields[1] == "list") {
		entries, warnings := cli.ListReadablePaths()
		var b strings.Builder
		if zh {
			b.WriteString("工作区外的可读路径（读取不再询问）：")
			if len(entries) == 0 {
				b.WriteString("无")
			}
			for _, e := range entries {
				fmt.Fprintf(&b, "\n  %-40s %s", cli.DisplayHomePath(e.Path), cli.ReadableSourceLabel(e.Source, true))
			}
			b.WriteString("\n添加：/boundary readable add <路径>　删除：/boundary readable rm <路径>")
		} else {
			b.WriteString("readable paths outside the workspace (reads no longer ask):")
			if len(entries) == 0 {
				b.WriteString(" none")
			}
			for _, e := range entries {
				fmt.Fprintf(&b, "\n  %-40s %s", cli.DisplayHomePath(e.Path), cli.ReadableSourceLabel(e.Source, false))
			}
			b.WriteString("\nadd: /boundary readable add <path>  remove: /boundary readable rm <path>")
		}
		for _, w := range warnings {
			fmt.Fprintf(&b, "\nwarning: %s", w)
		}
		return b.String()
	}
	if len(fields) >= 2 {
		switch fields[1] {
		case "add":
			p := restAfter("add")
			if p == "" {
				return usage()
			}
			root, err := cli.AddReadablePath(p)
			if err != nil {
				if zh {
					return "无法添加：" + err.Error()
				}
				return "cannot add: " + err.Error()
			}
			display := cli.DisplayHomePath(root)
			if zh {
				return fmt.Sprintf("已授权读取 %s，已写入 grants.json（重启后仍生效）", display)
			}
			return fmt.Sprintf("granted read access to %s, saved to grants.json", display)
		case "rm", "remove":
			p := restAfter(fields[1])
			if p == "" {
				return usage()
			}
			res, err := cli.RemoveReadablePath(p)
			if err != nil {
				if zh {
					return "无法删除：" + err.Error()
				}
				return "cannot remove: " + err.Error()
			}
			display := cli.DisplayHomePath(res.Path)
			if !res.Found() {
				if zh {
					return fmt.Sprintf("未授权该路径：%s", display)
				}
				return fmt.Sprintf("not granted: %s", display)
			}
			var b strings.Builder
			if zh {
				fmt.Fprintf(&b, "已撤销 %s", display)
				if res.WasRemembered {
					b.WriteString("，已从 grants.json 删除")
				}
			} else {
				fmt.Fprintf(&b, "revoked %s", display)
				if res.WasRemembered {
					b.WriteString(", deleted from grants.json")
				}
			}
			return b.String()
		}
	}
	return usage()
}
