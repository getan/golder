package extensions

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"

	"github.com/getan/golder/internal/filelock"
	"github.com/getan/golder/internal/provider"
)

// LarkSetup holds one transient operator-entered configuration. AppSecret is
// never put into the source table, transcript, command line or model context.
type LarkSetup struct {
	Package     ManagedPackage
	LarkCLI     string
	ProjectCLI  string
	AppID       string
	AppSecret   string
	Domains     []string
	ProjectHost string
}

// AddLark writes one new, disabled Lark source after validating its installed
// helper and executable dependencies. Existing sources are never overwritten.
func (m *Manager) AddLark(ctx context.Context, setup LarkSetup) (err error) {
	if m == nil || m.closed.Load() {
		return errors.New("extension manager unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if setup.AppSecret == "" || setup.AppSecret != strings.TrimSpace(setup.AppSecret) {
		return errors.New("enter a valid Lark App Secret")
	}
	ref := SourceRef{Kind: "mcp", ID: "lark"}
	name := "mcp-lark-" + strings.ToLower(rand.Text()[:16])
	cfg, err := LarkSourceConfig(LarkSourceOptions{
		Package: setup.Package, LarkCLI: setup.LarkCLI, ProjectCLI: setup.ProjectCLI,
		AppID: setup.AppID, AppSecretCredential: name, Domains: setup.Domains, ProjectHost: setup.ProjectHost,
	})
	if err != nil {
		return err
	}
	if _, err := fingerprint(ref, cfg); err != nil {
		return fmt.Errorf("cannot review Lark dependencies: %w", err)
	}
	if err := provider.CreateCredential(provider.CredentialFilePath(), name, setup.AppSecret); err != nil {
		return errors.New("could not save Lark credential")
	}
	committed := false
	defer func() {
		if !committed {
			_ = provider.DeleteCredential(provider.CredentialFilePath(), name)
		}
	}()
	configWriteMu.Lock()
	defer configWriteMu.Unlock()
	unlock, err := filelock.Acquire(m.opts.ConfigPath)
	if err != nil {
		return err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := LoadConfig(m.opts.ConfigPath)
	if err != nil {
		return err
	}
	if _, exists := current.MCP.Servers[ref.ID]; exists || m.HasSource(ref) {
		return errors.New("Lark is already configured; use its existing settings")
	}
	if err := writeSourceLocked(m.opts.ConfigPath, ref, &cfg); err != nil {
		return err
	}
	committed = true
	if err := m.ConfigureSource(ref, cfg); err != nil {
		return fmt.Errorf("Lark saved disabled; reload extensions: %w", err)
	}
	return nil
}
