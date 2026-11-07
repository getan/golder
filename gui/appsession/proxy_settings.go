package appsession

import (
	"errors"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strings"

	"github.com/getan/golder/internal/cli/config"
	"github.com/getan/golder/internal/provider"
)

// DefaultProxyURL is a suggested Clash Verge mixed-port address. Routes stay
// off until explicitly selected, and the user may replace the address.
const DefaultProxyURL = "http://127.0.0.1:7897"

type ProxyProvider struct {
	Name    string
	Enabled bool
}

// ProxySettings is a local editor for the shared /proxy configuration. The
// original snapshot detects changes made before saving without exposing credentials.
type ProxySettings struct {
	URL             string
	EffectiveURL    string
	EnvOverride     bool
	Browser         bool
	Providers       []ProxyProvider
	original        config.ProxyConfig
	initialSelected []string
}

func LoadProxySettings() (ProxySettings, error) {
	raw, err := config.LoadProxyConfig()
	if err != nil {
		return ProxySettings{}, err
	}
	resolved, err := provider.ProxySettings()
	if err != nil {
		return ProxySettings{}, err
	}
	address := strings.TrimSpace(raw.URL)
	if address != "" {
		parsed, err := url.Parse(address)
		if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return ProxySettings{}, errors.New("saved proxy URL is invalid or contains credentials; configure authenticated proxies with GOLDER_PROXY")
		}
	} else {
		address = DefaultProxyURL
	}
	setting := ProxySettings{
		URL: address, Browser: raw.Browser,
		EnvOverride: strings.TrimSpace(os.Getenv("GOLDER_PROXY")) != "",
		original:    raw, initialSelected: slices.Clone(resolved.Providers),
	}
	if effective := provider.ProxyURL(); effective != "" {
		u, err := url.Parse(effective)
		if err != nil || provider.ValidateProxyURL(effective) != nil {
			setting.EffectiveURL = "(invalid URL)"
		} else {
			u.User, u.Path, u.RawPath, u.RawQuery, u.Fragment = nil, "", "", "", ""
			setting.EffectiveURL = u.String()
		}
	}
	seen := make(map[string]bool)
	for _, spec := range provider.ProviderSpecs() {
		setting.Providers = append(setting.Providers, ProxyProvider{Name: spec.Name, Enabled: slices.Contains(resolved.Providers, spec.Name)})
		seen[spec.Name] = true
	}
	// Preserve selections for plugins or older/newer versions unknown here.
	for _, name := range resolved.Providers {
		if !seen[name] {
			setting.Providers = append(setting.Providers, ProxyProvider{Name: name, Enabled: true})
			seen[name] = true
		}
	}
	return setting, nil
}

func SaveProxySettings(edit ProxySettings) (ProxySettings, error) {
	raw, err := config.LoadProxyConfig()
	if err != nil {
		return ProxySettings{}, err
	}
	if !reflect.DeepEqual(raw, edit.original) {
		return ProxySettings{}, errors.New("proxy settings changed elsewhere; reopen Settings and try again")
	}
	address := strings.TrimSpace(edit.URL)
	if err := provider.ValidateProxyURL(address); err != nil {
		return ProxySettings{}, err
	}
	parsed, err := url.Parse(address)
	if err != nil || parsed.User != nil {
		return ProxySettings{}, errors.New("do not save proxy credentials here; use GOLDER_PROXY instead")
	}
	if edit.Browser {
		// Chromium receives this URL in its process arguments. Validate both the
		// saved value and the currently effective environment override.
		for _, value := range []string{address, strings.TrimSpace(os.Getenv("GOLDER_PROXY"))} {
			if value == "" {
				continue
			}
			if err := provider.ValidateProxyURL(value); err != nil {
				return ProxySettings{}, err
			}
			u, err := url.Parse(value)
			if err != nil || u.User != nil || (u.Path != "" && u.Path != "/") {
				return ProxySettings{}, errors.New("browser proxy cannot contain credentials or a path")
			}
		}
	}
	selected := make([]string, 0, len(edit.Providers))
	for _, entry := range edit.Providers {
		if entry.Enabled && !slices.Contains(selected, entry.Name) {
			selected = append(selected, entry.Name)
		}
	}
	if override := strings.TrimSpace(os.Getenv("GOLDER_PROXY")); len(selected) > 0 && override != "" {
		if err := provider.ValidateProxyURL(override); err != nil {
			return ProxySettings{}, errors.New("GOLDER_PROXY overrides the saved URL but is invalid; fix it or disable provider proxy routing")
		}
	}
	initial := slices.Clone(edit.initialSelected)
	current := slices.Clone(selected)
	slices.Sort(initial)
	slices.Sort(current)
	// Saving the first address changes how legacy nil selections are resolved.
	// Freeze the selection shown in the editor rather than silently changing it.
	materializeSelection := raw.Providers == nil && strings.TrimSpace(raw.URL) == ""
	if !slices.Equal(current, initial) || materializeSelection {
		raw.Providers = selected
	}
	raw.URL, raw.Browser = address, edit.Browser
	if err := config.SaveProxyConfig(raw); err != nil {
		return ProxySettings{}, err
	}
	return LoadProxySettings()
}
