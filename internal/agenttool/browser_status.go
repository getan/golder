package agenttool

import (
	"runtime"
	"strings"

	"github.com/getan/golder/internal/browserctl"
)

const (
	browserStatusUnknown    = "unknown"
	browserProxyModeDirect  = "direct"
	browserProxyModeProxy   = "proxy"
	browserProxyModeUnknown = browserStatusUnknown
)

type browserManualLoginDiagnostics struct {
	CloseProfileFirst bool     `json:"close_profile_first"`
	RequiredFlags     []string `json:"required_flags"`
	SameProxyRequired bool     `json:"same_proxy_required"`
}

type browserRuntimeStatus struct {
	ProfileMode         string                        `json:"profile_mode"`
	PersistentProfile   string                        `json:"persistent_profile,omitempty"`
	ProfileDir          string                        `json:"profile_dir,omitempty"`
	ProfileInUse        bool                          `json:"profile_in_use"`
	RuntimeMode         string                        `json:"runtime_mode"`
	CookieStore         string                        `json:"cookie_store"`
	MockKeychain        bool                          `json:"mock_keychain"`
	ProxyMode           string                        `json:"proxy_mode"`
	ConfiguredProxyMode string                        `json:"configured_proxy_mode"`
	ManualLogin         browserManualLoginDiagnostics `json:"manual_login"`
}

// browserRuntimeDiagnostics adds only launch/runtime facts to interaction.status.
// It never opens Chromium's profile databases and never returns cookie names or
// values. The profile path is derived from the already-authorized logical login
// profile rather than accepted from page data or repository metadata.
func (t *BrowserTool) browserRuntimeDiagnostics(info browserctl.Info) browserRuntimeStatus {
	flags := []string{"--password-store=basic"}
	if runtime.GOOS == "darwin" {
		flags = append(flags, "--use-mock-keychain")
	}
	status := browserRuntimeStatus{
		ProfileMode:  "private-or-external",
		ProfileInUse: info.BrowserID != "",
		RuntimeMode:  "external",
		CookieStore:  browserStatusUnknown,
		ProxyMode:    browserProxyModeUnknown,
		ManualLogin:  browserManualLoginDiagnostics{RequiredFlags: []string{}},
	}

	prefix := browserProjectKey(t.cwd) + "/"
	if strings.HasPrefix(info.LoginProfile, prefix) {
		name := strings.TrimPrefix(info.LoginProfile, prefix)
		if browserProfileName.MatchString(name) && t.Control.profileAllowed(name) {
			status.PersistentProfile = name
			status.ProfileMode = "project-named"
			if name == sharedBrowserProfileName {
				status.ProfileMode = "project-default"
			}
			if profile, err := persistentBrowserProfile(t.cwd, name, false); err == nil {
				status.ProfileDir = profile
			}
			status.ManualLogin.CloseProfileFirst = true
			status.ManualLogin.SameProxyRequired = true
		}
	}

	if t.Control != nil {
		t.Control.mu.Lock()
		owned, ok := t.Control.browsers[info.BrowserID]
		t.Control.mu.Unlock()
		if ok {
			status.RuntimeMode = "controlled"
			if owned.browserRuntime {
				status.RuntimeMode = "controlled-headless"
			}
			if owned.proxyMode != "" {
				status.ProxyMode = owned.proxyMode
			}
		}
	}

	if status.RuntimeMode != "external" {
		status.CookieStore = "basic"
		status.MockKeychain = runtime.GOOS == "darwin"
		if status.PersistentProfile != "" {
			status.ManualLogin.RequiredFlags = flags
		}
	}
	status.ConfiguredProxyMode = t.configuredBrowserProxyMode()
	return status
}

func (t *BrowserTool) configuredBrowserProxyMode() string {
	if t.Control == nil || t.Control.BrowserProxy == nil {
		return browserProxyModeDirect
	}
	proxy, err := t.Control.BrowserProxy()
	if err != nil {
		return "invalid"
	}
	if proxy == "" {
		return browserProxyModeDirect
	}
	return browserProxyModeProxy
}
