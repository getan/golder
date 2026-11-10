package agenttool

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/browserctl"
	"github.com/getan/golder/internal/golderhome"
	"github.com/getan/golder/internal/privatepath"
)

var browserProfileName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// The project default is intentionally shared by the person and every agent
// working in this project. Other named profiles retain exact per-agent consent.
const sharedBrowserProfileName = "default"

func effectiveBrowserProfileName(name string) string {
	if name == "" {
		return sharedBrowserProfileName
	}
	return name
}

func browserLoginProfile(root, name string) string {
	return browserProjectKey(root) + "/" + effectiveBrowserProfileName(name)
}

// persistentBrowserProfile resolves only a dedicated, project-scoped profile.
// No caller can supply a filesystem path or select a personal Chrome profile.
// It stays outside the checkout, where ordinary agent file tools cannot read it.
func persistentBrowserProfile(root, name string, create bool) (string, error) {
	if !browserProfileName.MatchString(name) {
		return "", errors.New("browser profile must be a name of 1–64 lowercase letters, digits, - or _, starting with a letter")
	}
	home := golderhome.Dir()
	if home == "" {
		return "", errors.New("private browser profile storage is unavailable")
	}
	dir := filepath.Join(home, "browser-profiles", browserProjectKey(root))
	profile := filepath.Join(dir, name+".profile")
	if insidePath(root, profile) {
		return "", errors.New("saved browser logins must live outside the checkout; the current workspace contains Golder's private browser profile storage. Select a narrower project folder in the GUI that does not contain GOLDER_HOME (normally ~/.golder), then open the controlled browser again. Do not automatically relocate GOLDER_HOME or switch to an unmanaged system browser")
	}
	if create {
		for _, path := range []string{home, filepath.Join(home, "browser-profiles"), dir, profile} {
			if err := privatepath.EnsureDir(path); err != nil {
				return "", err
			}
		}
	}
	return profile, nil
}

func (g *ControlGuard) profileAllowed(name string) bool {
	if g == nil {
		return false
	}
	if name == sharedBrowserProfileName {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.browserProfiles[name]
}

func (t *BrowserTool) checkProfileAccess(info browserctl.Info) error {
	if info.LoginProfile == "" {
		return nil
	}
	name := strings.TrimPrefix(info.LoginProfile, browserProjectKey(t.cwd)+"/")
	if !browserProfileName.MatchString(name) || !t.Control.profileAllowed(name) {
		return errors.New("this browser holds saved logins; this agent must explicitly open the named profile and obtain your confirmation before reading or controlling it")
	}
	return nil
}

// RequiresOperatorConfirmationFor is a trusted, per-call policy, not a model
// argument. The project default is deliberately shared; a fresh agent must get
// a person's answer before accessing any other named login profile. The
// ordinary auto reviewer and even full-access mode cannot invent it.
func (t *BrowserTool) RequiresOperatorConfirmationFor(raw json.RawMessage) bool {
	var a browserArgs
	_ = json.Unmarshal(raw, &a)
	return a.Action == "resume" || a.Profile != "" && !t.Control.profileAllowed(a.Profile)
}

func (t *BrowserTool) authorizeProfile(ctx context.Context, id string, raw json.RawMessage, name string) error {
	if name == "" || t.Control.profileAllowed(name) {
		return nil
	}
	if !agentcore.OperatorConfirmedFor(ctx, id, t.Name(), raw) {
		return errors.New("using saved browser logins requires your exact confirmation; switch /permissions to ask and approve this profile for this agent session")
	}
	t.Control.mu.Lock()
	defer t.Control.mu.Unlock()
	if t.Control.browserProfiles == nil {
		t.Control.browserProfiles = make(map[string]bool)
	}
	t.Control.browserProfiles[name] = true
	return nil
}
