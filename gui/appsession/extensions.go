package appsession

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"

	setupevent "github.com/getan/golder/extension/setup"
	"github.com/getan/golder/internal/extensions"
	"github.com/getan/golder/internal/provider"
)

var larkSourceRef = extensions.SourceRef{Kind: "mcp", ID: "lark"}

var discoverOfficialCLI = extensions.DiscoverOfficialCLI
var installOfficialCLI = func(ctx context.Context, kind extensions.OfficialCLIKind) (extensions.OfficialCLI, error) {
	return extensions.NewOfficialCLIInstaller().Install(ctx, kind)
}

// ExtensionPackageView is the compact package/source state the native Settings
// page renders. It contains no environment values, credentials or setup URLs.
type ExtensionPackageView struct {
	ID               string
	Name             string
	Description      string
	Installed        bool
	Configured       bool
	Enabled          bool
	Trusted          bool
	Version          string
	BundledVersion   string
	InstalledVersion string
	ActiveVersion    string
	ActiveManaged    bool
	UpdateAvailable  bool
	State            string
	Authentication   string
	LastError        string
}

// ExtensionSourceView is one configured MCP/CLI/RPC source in the manager.
type ExtensionSourceView struct {
	Kind           string
	ID             string
	Type           string
	Enabled        bool
	Trusted        bool
	State          string
	Authentication string
	Version        string
	LastError      string
	Endpoint       string
	Network        bool
	StateDir       bool
	Executables    int
	Tools          int
}

// LarkExtensionSettings is the non-secret editable Lark configuration.
type LarkExtensionSettings struct {
	ExtensionPackageView
	LarkCLI             string
	ProjectCLI          string
	SavedLarkCLI        string
	SavedProjectCLI     string
	SavedProjectHost    string
	AppID               string
	CredentialName      string
	CredentialAvailable bool
	Domains             []string
	ProjectHost         string
}

// LarkExtensionEdit carries one configuration save. NewAppSecret is write-only
// and never returned by ExtensionSettings.
type LarkExtensionEdit struct {
	LarkCLI        string
	ProjectCLI     string
	AppID          string
	CredentialName string
	NewAppSecret   string
	Domains        []string
	ProjectHost    string
}

// ExtensionSetupEvent is the already-sanitized operator event the GUI may show.
type ExtensionSetupEvent struct {
	Type            string
	Service         string
	Message         string
	VerificationURL string
	UserCode        string
	Authenticated   bool
	Phase           string
	Code            string
	Retryable       bool
}

// ExtensionSettings returns package and live source state without starting an
// extension process.
func (c *Controller) ExtensionSettings() (LarkExtensionSettings, error) {
	c.mu.Lock()
	manager := c.opt.Extensions
	desktopVersion := c.opt.Version
	c.mu.Unlock()
	if manager == nil {
		return LarkExtensionSettings{}, errors.New("extension manager unavailable")
	}
	view := LarkExtensionSettings{
		ExtensionPackageView: ExtensionPackageView{
			ID: "lark", Name: "Lark + Project",
			Description: "Feishu/Lark documents, messages, Sheets, Base and Project work items.",
			State:       "not installed", BundledVersion: managedExtensionVersion(desktopVersion),
		},
		Domains: []string{"docs", "drive", "im", "sheets", "base"}, ProjectHost: "project.feishu.cn",
	}
	if pkg, ok := extensions.InstalledManagedPackage("lark"); ok {
		view.Installed, view.Version, view.InstalledVersion, view.State = true, pkg.Version, pkg.Version, "installed · not configured"
	}
	if snapshot, ok := manager.Source(larkSourceRef); ok {
		view.Configured = true
		view.Enabled = snapshot.Enabled
		view.Trusted = snapshot.Trust == "approved"
		view.State, view.Authentication, view.LastError = snapshot.State, snapshot.Authentication, snapshot.LastError
		cfg, err := manager.Config(larkSourceRef)
		if err != nil {
			return view, err
		}
		view.LarkCLI = cfg.Env["LARK_MCP_CLI"]
		view.ProjectCLI = cfg.Env["LARK_MCP_PROJECT_CLI"]
		view.SavedLarkCLI = view.LarkCLI
		view.SavedProjectCLI = view.ProjectCLI
		view.SavedProjectHost = strings.TrimSpace(cfg.Env["MEEGLE_HOST"])
		if view.SavedProjectHost == "" {
			view.SavedProjectHost = "project.feishu.cn"
		}
		view.AppID = cfg.Env["LARK_MCP_APP_ID"]
		view.CredentialName = credentialReferenceName(cfg.Env["LARK_MCP_APP_SECRET"])
		if domains := strings.TrimSpace(cfg.Env["LARK_MCP_DOMAINS"]); domains != "" {
			view.Domains = splitCSV(domains)
		}
		if host := strings.TrimSpace(cfg.Env["MEEGLE_HOST"]); host != "" {
			view.ProjectHost = host
		}
		if active, managed := extensions.ManagedPackageForExecutable("lark", cfg.Command); managed {
			view.ActiveVersion, view.ActiveManaged = active.Version, true
		} else {
			view.ActiveVersion = strings.TrimSpace(snapshot.Version)
		}
		if view.ActiveVersion != "" {
			view.Version = view.ActiveVersion
		}
	}
	if view.BundledVersion != "" && view.BundledVersion != "dev" {
		if view.Configured {
			view.UpdateAvailable = !view.ActiveManaged || view.ActiveVersion != view.BundledVersion
		} else {
			view.UpdateAvailable = view.InstalledVersion != view.BundledVersion
		}
	}
	if view.CredentialName != "" {
		_, err := provider.ResolveCredentialReference(provider.CredentialFilePath(), view.CredentialName)
		view.CredentialAvailable = err == nil
	}
	view.LarkCLI, view.ProjectCLI = c.DiscoverLarkCLIs(view.LarkCLI, view.ProjectCLI)
	return view, nil
}

// DiscoverLarkCLIs performs bounded local discovery only. Script/npm wrappers
// are ignored; returned paths are resolved native executables.
func (c *Controller) DiscoverLarkCLIs(preferredLark, preferredProject string) (larkCLI, projectCLI string) {
	larkCLI, projectCLI, _, _ = c.DetectLarkCLIs(preferredLark, preferredProject)
	return larkCLI, projectCLI
}

// DetectLarkCLIs is DiscoverLarkCLIs with explicit match flags for UI feedback.
// A configured but invalid preferred path is preserved, but is not reported as
// detected.
func (c *Controller) DetectLarkCLIs(preferredLark, preferredProject string) (larkCLI, projectCLI string, larkFound, projectFound bool) {
	larkCLI, projectCLI = preferredLark, preferredProject
	if found, ok := discoverOfficialCLI(extensions.OfficialLarkCLI, preferredLark); ok {
		larkCLI = found.Executable
		larkFound = true
	}
	if found, ok := discoverOfficialCLI(extensions.OfficialProjectCLI, preferredProject); ok {
		projectCLI = found.Executable
		projectFound = true
	}
	return larkCLI, projectCLI, larkFound, projectFound
}

// InstallOfficialLarkCLI downloads one pinned official native distribution,
// verifies its release integrity and installs it without npm or Node.
func (c *Controller) InstallOfficialLarkCLI(ctx context.Context, project bool) (string, error) {
	kind := extensions.OfficialLarkCLI
	if project {
		kind = extensions.OfficialProjectCLI
	}
	installed, err := installOfficialCLI(ctx, kind)
	if err != nil {
		return "", err
	}
	return installed.Executable, nil
}

// ExtensionSources lists every configured source without executing it.
func (c *Controller) ExtensionSources() []ExtensionSourceView {
	c.mu.Lock()
	manager := c.opt.Extensions
	c.mu.Unlock()
	if manager == nil {
		return nil
	}
	snapshots := manager.List()
	out := make([]ExtensionSourceView, 0, len(snapshots))
	for _, snapshot := range snapshots {
		out = append(out, ExtensionSourceView{
			Kind: snapshot.Ref.Kind, ID: snapshot.Ref.ID, Type: snapshot.Type,
			Enabled: snapshot.Enabled, Trusted: snapshot.Trust == "approved",
			State: snapshot.State, Authentication: snapshot.Authentication,
			Version: snapshot.Version, LastError: snapshot.LastError, Endpoint: snapshot.Endpoint,
			Network:     capabilityBool(snapshot.Capabilities, "network"),
			StateDir:    capabilityBool(snapshot.Capabilities, "state_dir"),
			Executables: capabilityCount(snapshot.Capabilities, "executables"), Tools: len(snapshot.Tools),
		})
	}
	return out
}

func capabilityBool(values map[string]any, key string) bool {
	value, _ := values[key].(bool)
	return value
}

func capabilityCount(values map[string]any, key string) int {
	switch value := values[key].(type) {
	case []any:
		return len(value)
	case []string:
		return len(value)
	default:
		return 0
	}
}

// SetExtensionEnabled is the generic explicit enable/disable action for an
// already-configured source. Enabling fingerprints and remembers approval.
func (c *Controller) SetExtensionEnabled(kind, id string, enabled bool) error {
	manager, release, err := c.beginMCPMutation()
	if err != nil {
		return err
	}
	defer release()
	ref := extensions.SourceRef{Kind: kind, ID: id}
	if kind == "mcp" {
		reviewed, err := manager.PrepareMCPEdit(id)
		if err != nil {
			return err
		}
		if enabled {
			return manager.EnableReviewedMCP(reviewed, true)
		}
		return manager.DisableReviewedMCP(reviewed)
	}
	if enabled {
		if err := manager.Approve(ref); err != nil {
			return err
		}
		if err := manager.EnabledSourceConfig(ref, true); err != nil {
			return err
		}
		return manager.Enable(ref)
	}
	if err := manager.Disable(ref); err != nil {
		return err
	}
	return manager.EnabledSourceConfig(ref, false)
}

func (c *Controller) TestExtension(ctx context.Context, kind, id string) error {
	manager, release, err := c.beginMCPMutation()
	if err != nil {
		return err
	}
	defer release()
	return manager.Connect(ctx, extensions.SourceRef{Kind: kind, ID: id})
}

// InstallLarkPackage explicitly installs the signed GUI helper. It configures
// or executes nothing.
func (c *Controller) InstallLarkPackage(ctx context.Context, source, checksum, version string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := extensions.InstallManagedPackage(extensions.PackageInstallOptions{
		ID: "lark", Source: source, SHA256: checksum, Version: version,
	})
	return err
}

// UpdateLarkPackage installs the verified bundled helper and rebuilds the
// existing Source around that exact package. Existing non-secret settings,
// named credential references and source-scoped login HOME are preserved; the
// replacement Source is disabled and must be explicitly approved again.
func (c *Controller) UpdateLarkPackage(ctx context.Context, source, checksum, version string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	manager, release, err := c.beginMCPMutation()
	if err != nil {
		return err
	}
	defer release()
	current, err := manager.Config(larkSourceRef)
	if err != nil {
		return errors.New("configure Lark before updating its helper")
	}
	pkg, err := extensions.InstallManagedPackage(extensions.PackageInstallOptions{
		ID: "lark", Source: source, SHA256: checksum, Version: version,
	})
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	updated, err := larkSourceConfigFromExisting(pkg, current)
	if err != nil {
		return err
	}
	return manager.ReplaceSourceConfig(larkSourceRef, updated)
}

// ConfigureLark stores a new App Secret by name, writes a disabled source, then
// updates the live manager without granting trust or starting code.
func (c *Controller) ConfigureLark(ctx context.Context, edit LarkExtensionEdit) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	manager, release, err := c.beginMCPMutation()
	if err != nil {
		return err
	}
	defer release()
	pkg, ok := extensions.InstalledManagedPackage("lark")
	if !ok {
		return errors.New("install the managed golder-lark package first")
	}
	credentialName := strings.TrimSpace(edit.CredentialName)
	value := strings.TrimSpace(edit.NewAppSecret)
	if value != "" && credentialName == "" {
		credentialName = "lark-app-secret"
	}
	if value == "" && credentialName == "" {
		return errors.New("enter an App Secret or choose a named credential")
	}
	makeConfig := func() (extensions.SourceConfig, error) {
		return extensions.LarkSourceConfig(extensions.LarkSourceOptions{
			Package: pkg, LarkCLI: edit.LarkCLI, ProjectCLI: edit.ProjectCLI,
			AppID: edit.AppID, AppSecretCredential: credentialName,
			Domains: edit.Domains, ProjectHost: edit.ProjectHost,
		})
	}
	cfg, err := makeConfig()
	if err != nil {
		return err
	}
	created, committed := false, false
	defer func() {
		if created && !committed {
			_ = provider.DeleteCredential(provider.CredentialFilePath(), credentialName)
		}
	}()
	if value != "" {
		err = provider.CreateCredential(provider.CredentialFilePath(), credentialName, value)
		if errors.Is(err, provider.ErrCredentialExists) {
			credentialName = "lark-app-" + strings.ToLower(rand.Text()[:16])
			cfg, err = makeConfig()
			if err == nil {
				err = provider.CreateCredential(provider.CredentialFilePath(), credentialName, value)
			}
		}
		if err != nil {
			return err
		}
		created = true
	} else if _, err := provider.ResolveCredentialReference(provider.CredentialFilePath(), credentialName); err != nil {
		return fmt.Errorf("credential %q is not stored", credentialName)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	err = manager.ReplaceSourceConfig(larkSourceRef, cfg)
	committed = err == nil || errors.Is(err, extensions.ErrSourceCommitted)
	return err
}

// EnableLark is the explicit review boundary. It fingerprints the helper and
// official CLI dependencies, remembers approval, persists enabled=true and only
// then admits the source to the current session.
func (c *Controller) EnableLark() error {
	manager, release, err := c.beginMCPMutation()
	if err != nil {
		return err
	}
	defer release()
	if err := c.requireMatchingLarkHelper(); err != nil {
		return err
	}
	reviewed, err := manager.PrepareMCPEdit(larkSourceRef.ID)
	if err != nil {
		return err
	}
	return manager.EnableReviewedMCP(reviewed, true)
}

func (c *Controller) DisableLark() error {
	manager, release, err := c.beginMCPMutation()
	if err != nil {
		return err
	}
	defer release()
	reviewed, err := manager.PrepareMCPEdit(larkSourceRef.ID)
	if err != nil {
		return err
	}
	return manager.DisableReviewedMCP(reviewed)
}

// AuthorizeLark runs the reviewed setup command and forwards only sanitized
// events. service is "lark" or the declared "project" variant.
func (c *Controller) AuthorizeLark(ctx context.Context, service string, show func(ExtensionSetupEvent)) error {
	if err := c.requireMatchingLarkHelper(); err != nil {
		return err
	}
	c.mu.Lock()
	manager := c.opt.Extensions
	c.mu.Unlock()
	if manager == nil {
		return errors.New("extension manager unavailable")
	}
	variant := ""
	if service == "project" {
		variant = "project"
	} else if service != "lark" {
		return errors.New("unknown authorization service")
	}
	return manager.SetupVariant(ctx, larkSourceRef, variant, func(event setupevent.Event) error {
		if show != nil {
			show(ExtensionSetupEvent{
				Type: event.Type, Service: event.Service, Message: event.Message,
				VerificationURL: event.VerificationURL, UserCode: event.UserCode, Authenticated: event.Authenticated,
				Phase: event.Phase, Code: event.Code, Retryable: event.Retryable,
			})
		}
		return nil
	})
}

func (c *Controller) TestLark(ctx context.Context) error {
	manager, release, err := c.beginMCPMutation()
	if err != nil {
		return err
	}
	defer release()
	if err := c.requireMatchingLarkHelper(); err != nil {
		return err
	}
	return manager.Connect(ctx, larkSourceRef)
}

func (c *Controller) ResetLark() error {
	manager, release, err := c.beginMCPMutation()
	if err != nil {
		return err
	}
	defer release()
	return manager.Reset(larkSourceRef)
}

// UninstallLark removes source configuration and the managed helper, but keeps
// server-side consent and named App Secret. Reset and credential removal are
// separate explicit destructive operations.
func (c *Controller) UninstallLark() error {
	manager, release, err := c.beginMCPMutation()
	if err != nil {
		return err
	}
	defer release()
	if manager.HasSource(larkSourceRef) {
		reviewed, err := manager.PrepareMCPEdit(larkSourceRef.ID)
		if err != nil {
			return err
		}
		if err := manager.RemoveMCP(reviewed); err != nil {
			return err
		}
	}
	return extensions.RemoveAllManagedPackages("lark")
}

func credentialReferenceName(value string) string {
	const prefix = "${credential:"
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, prefix) && strings.HasSuffix(value, "}") {
		return strings.TrimSuffix(strings.TrimPrefix(value, prefix), "}")
	}
	return ""
}

func managedExtensionVersion(version string) string {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	if version == "" || version == "unknown" {
		return "dev"
	}
	return strings.ReplaceAll(version, "+", "-")
}

func larkSourceConfigFromExisting(pkg extensions.ManagedPackage, current extensions.SourceConfig) (extensions.SourceConfig, error) {
	credential := credentialReferenceName(current.Env["LARK_MCP_APP_SECRET"])
	return extensions.LarkSourceConfig(extensions.LarkSourceOptions{
		Package: pkg, LarkCLI: current.Env["LARK_MCP_CLI"], ProjectCLI: current.Env["LARK_MCP_PROJECT_CLI"],
		AppID: current.Env["LARK_MCP_APP_ID"], AppSecretCredential: credential,
		Domains: splitCSV(current.Env["LARK_MCP_DOMAINS"]), ProjectHost: current.Env["MEEGLE_HOST"],
	})
}

func (c *Controller) requireMatchingLarkHelper() error {
	settings, err := c.ExtensionSettings()
	if err != nil {
		return err
	}
	if settings.BundledVersion == "dev" {
		return nil
	}
	if !settings.Configured || settings.UpdateAvailable || !settings.ActiveManaged || settings.ActiveVersion != settings.BundledVersion {
		return errors.New("update the managed Lark helper before approval, authorization or connection testing")
	}
	return nil
}

func splitCSV(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
