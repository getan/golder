package prompts

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/getan/golder/internal/cli"
	"github.com/getan/golder/internal/cli/config"
	"github.com/getan/golder/internal/provider"
	"github.com/getan/golder/internal/runtime"
)

// ProviderEndpointParts breaks the endpoint summary into its structured
// pieces: the consulted base-url env var(s), the effective URL, where it came
// from ("default", a variable name, or "base_url override"), and the proxy
// routing. It backs both the one-line text listing (ProviderEndpointSummary)
// and the TUI picker's labeled detail block.
func ProviderEndpointParts(live *cli.LiveConfig, spec provider.ProviderSpec) (envVar, baseURL, source, proxy string) {
	override := ""
	if live != nil && live.ProviderName == spec.Name {
		override = live.BaseURL
	}
	base := provider.ResolveBaseURL(spec, override, os.Getenv)
	source = "default"
	if override != "" {
		source = "base_url override"
	} else {
		for _, name := range provider.BaseURLEnvVars(spec) {
			if strings.TrimSpace(os.Getenv(name)) != "" {
				source = name
				break
			}
		}
	}
	return strings.Join(provider.BaseURLEnvVars(spec), " / "),
		provider.DisplayURL(base), source, provider.ProxyStatus(spec.Name, base)
}

// ProviderEndpointSummary is shared by the terminal picker and text listing.
func ProviderEndpointSummary(live *cli.LiveConfig, spec provider.ProviderSpec) string {
	envVar, base, source, proxy := ProviderEndpointParts(live, spec)
	return fmt.Sprintf("%s (optional) · %s [%s] · %s",
		envVar, base, source, proxy)
}

// ProxyProvidersSelectedFirst keeps registry order within the selected and
// unselected groups. Both the picker and text listing use this ordering.
func ProxyProvidersSelectedFirst(selected []string) []provider.ProviderSpec {
	specs := provider.ProviderSpecs()
	slices.SortStableFunc(specs, func(a, b provider.ProviderSpec) int {
		aSelected := slices.Contains(selected, a.Name)
		bSelected := slices.Contains(selected, b.Name)
		if aSelected == bSelected {
			return 0
		}
		if aSelected {
			return -1
		}
		return 1
	})
	return specs
}

// The /proxy grammar: the whole command (the hint, what the popup and /help
// advertise) and the provider form alone, which is what a bad on/off verb is
// answered with — narrower than the whole grammar because the address case is not
// what the reader got wrong. Declared together so the two spellings of the
// provider form cannot drift.
const (
	proxyTargetGrammar = "<provider>|browser on|off"
)

func registerProxyCommand(reg *runtime.SlashRegistry) {
	reg.AddBuiltin(runtime.SlashCommand{
		Name:         "proxy",
		Category:     runtime.CategoryModel,
		Description:  "choose which providers and browser panes use the proxy",
		ArgumentHint: "[<provider>|browser on|off | url <url>]",
		Action: func(args string) string {
			cfg, err := provider.ProxySettings()
			if err != nil {
				return "proxy: " + err.Error()
			}
			fields := strings.Fields(args)
			if len(fields) == 0 {
				address := provider.DisplayURL(provider.ProxyURL())
				if address == "" {
					address = "not configured; /proxy url http://127.0.0.1:7897"
				}
				var b strings.Builder
				fmt.Fprintf(&b, "proxy address: %s\n", address)
				browserMark := " "
				if cfg.Browser {
					browserMark = "x"
				}
				fmt.Fprintf(&b, "\n[%s] browser (Chromium; applies on next launch)", browserMark)
				for _, spec := range ProxyProvidersSelectedFirst(cfg.Providers) {
					mark := " "
					if slices.Contains(cfg.Providers, spec.Name) {
						mark = "x"
					}
					fmt.Fprintf(&b, "\n[%s] %s", mark, spec.Name)
				}
				fmt.Fprintf(&b, "\n\n/proxy <provider>|browser on|off · /proxy url <url>\nSaved in %s; providers apply on the next request, browser routing on the next browser launch. GOLDER_PROXY overrides the saved address.", config.ProxyConfigPath())
				return b.String()
			}
			if len(fields) != 2 {
				return usageLine(reg, "proxy", "en")
			}
			message := ""
			if fields[0] == "url" {
				if err := provider.ValidateProxyURL(fields[1]); err != nil {
					return "proxy: " + err.Error()
				}
				// Interactive command history is plain text; authenticated
				// addresses should be supplied through GOLDER_PROXY instead.
				if strings.Contains(fields[1], "@") {
					return "proxy: use GOLDER_PROXY for an authenticated proxy URL"
				}
				saved, err := config.LoadProxyConfig()
				if err != nil {
					return "proxy: " + err.Error()
				}
				saved.URL = fields[1]
				cfg = saved // changing the address must not select or clear routes
				message = "proxy address saved: " + provider.DisplayURL(cfg.URL)
				if strings.TrimSpace(os.Getenv("GOLDER_PROXY")) != "" {
					message += " (GOLDER_PROXY currently overrides this address)"
				}
			} else if fields[0] == "browser" {
				if fields[1] != "on" && fields[1] != "off" {
					return usageLineFor("proxy", proxyTargetGrammar, false)
				}
				saved, err := config.LoadProxyConfig()
				if err != nil {
					return "proxy: " + err.Error()
				}
				saved.Browser = fields[1] == "on"
				cfg = saved // browser and provider selections are independent
				message = "proxy: browser " + fields[1]
				if cfg.Browser && provider.ProxyURL() == "" {
					message += "; set an address with /proxy url http://127.0.0.1:7897"
				}
			} else {
				spec, ok := provider.LookupProviderSpec(fields[0])
				if !ok {
					return fmt.Sprintf("proxy: unknown provider %q", fields[0])
				}
				if fields[1] != "on" && fields[1] != "off" {
					return usageLineFor("proxy", proxyTargetGrammar, false)
				}
				cfg.Providers = slices.DeleteFunc(cfg.Providers, func(name string) bool { return name == spec.Name })
				if fields[1] == "on" {
					cfg.Providers = append(cfg.Providers, spec.Name)
				}
				message = fmt.Sprintf("proxy: %s %s", spec.Name, fields[1])
				if fields[1] == "on" && provider.ProxyURL() == "" {
					message += "; set an address with /proxy url http://127.0.0.1:7897"
				}
			}
			if err := config.SaveProxyConfig(cfg); err != nil {
				return "proxy: " + err.Error()
			}
			if fields[0] == "browser" {
				return message + "\nSaved; close and reopen existing browser panes to apply."
			}
			return message + "\nSaved; applies to the next request."
		},
	})
}
