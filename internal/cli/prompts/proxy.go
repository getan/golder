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

// ProviderEndpointSummary is shared by the terminal picker and text listing.
func ProviderEndpointSummary(live *cli.LiveConfig, spec provider.ProviderSpec) string {
	override := ""
	if live != nil && live.ProviderName == spec.Name {
		override = live.BaseURL
	}
	base := provider.ResolveBaseURL(spec, override, os.Getenv)
	source := "default"
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
	return fmt.Sprintf("%s (optional) · %s [%s] · %s",
		strings.Join(provider.BaseURLEnvVars(spec), " / "),
		provider.DisplayURL(base), source, provider.ProxyStatus(spec.Name, base))
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

func registerProxyCommand(reg *runtime.SlashRegistry) {
	reg.AddBuiltin(runtime.SlashCommand{
		Name: "proxy", Description: "select proxied providers: /proxy [<provider> on|off | url <url>]",
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
				for _, spec := range ProxyProvidersSelectedFirst(cfg.Providers) {
					mark := " "
					if slices.Contains(cfg.Providers, spec.Name) {
						mark = "x"
					}
					fmt.Fprintf(&b, "\n[%s] %s", mark, spec.Name)
				}
				fmt.Fprintf(&b, "\n\n/proxy <provider> on|off · /proxy url <url>\nSaved in %s; applies to chat and model discovery. GOLDER_PROXY overrides the saved address.", config.ProxyConfigPath())
				return b.String()
			}
			if len(fields) != 2 {
				return "usage: /proxy | /proxy <provider> on|off | /proxy url <url>"
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
				cfg.URL = fields[1]
				message = "proxy address saved: " + provider.DisplayURL(cfg.URL)
				if strings.TrimSpace(os.Getenv("GOLDER_PROXY")) != "" {
					message += " (GOLDER_PROXY currently overrides this address)"
				}
			} else {
				spec, ok := provider.LookupProviderSpec(fields[0])
				if !ok {
					return fmt.Sprintf("proxy: unknown provider %q", fields[0])
				}
				if fields[1] != "on" && fields[1] != "off" {
					return "usage: /proxy <provider> on|off"
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
			return message + "\nSaved; applies to the next request."
		},
	})
}
