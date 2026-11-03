package extensions

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/getan/golder/internal/golderhome"
)

type firstPartyPackage struct {
	Binary string
}

var firstPartyPackages = map[string]firstPartyPackage{
	"lark": {Binary: "golder-lark"},
}

var packageVersionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]{0,63}$`)
var credentialNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var appIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,255}$`)
var hostLabelPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

// ManagedPackage is one checksum-verified first-party helper installed below
// the protected Golder state root.
type ManagedPackage struct {
	ID         string `json:"id"`
	Version    string `json:"version"`
	Executable string `json:"executable"`
	SHA256     string `json:"sha256"`
	Root       string `json:"root"`
}

// PackageInstallOptions is fixed by the signed GUI package. ID is checked
// against the first-party registry; Source and SHA256 must refer to the bundled
// helper, never a URL or model-provided path.
type PackageInstallOptions struct {
	ID      string
	Version string
	Source  string
	SHA256  string
}

// InstallManagedPackage copies a signed-bundle helper into a versioned private
// directory and verifies its SHA-256 before the atomic directory rename.
func InstallManagedPackage(opts PackageInstallOptions) (ManagedPackage, error) {
	meta, ok := firstPartyPackages[opts.ID]
	if !ok {
		return ManagedPackage{}, errors.New("unknown first-party extension package")
	}
	if !packageVersionPattern.MatchString(opts.Version) {
		return ManagedPackage{}, errors.New("invalid extension package version")
	}
	checksumFields := strings.Fields(opts.SHA256)
	if len(checksumFields) == 0 {
		return ManagedPackage{}, errors.New("extension package checksum is missing or invalid")
	}
	expected := strings.ToLower(strings.TrimSpace(checksumFields[0]))
	if len(expected) != sha256.Size*2 {
		return ManagedPackage{}, errors.New("extension package checksum is missing or invalid")
	}
	if _, err := hex.DecodeString(expected); err != nil {
		return ManagedPackage{}, errors.New("extension package checksum is invalid")
	}
	source, err := checkedBundleExecutable(opts.Source)
	if err != nil {
		return ManagedPackage{}, fmt.Errorf("extension package source: %w", err)
	}
	base, err := ensureManagedPackageBase(opts.ID)
	if err != nil {
		return ManagedPackage{}, err
	}
	root := filepath.Join(base, opts.Version)
	name := executableName(meta.Binary)
	destination := filepath.Join(root, "bin", name)
	if installed, ok := loadManagedPackage(root, opts.ID); ok {
		if installed.SHA256 != expected {
			return ManagedPackage{}, errors.New("installed extension package checksum differs; choose a new version or uninstall explicitly")
		}
		return installed, nil
	}
	if _, err := os.Lstat(root); err == nil {
		return ManagedPackage{}, errors.New("extension package destination already exists but is invalid")
	} else if !os.IsNotExist(err) {
		return ManagedPackage{}, err
	}

	tmp, err := os.MkdirTemp(base, ".install-*")
	if err != nil {
		return ManagedPackage{}, err
	}
	defer os.RemoveAll(tmp)
	if err := os.Chmod(tmp, 0o700); err != nil {
		return ManagedPackage{}, err
	}
	binDir := filepath.Join(tmp, "bin")
	if err := os.Mkdir(binDir, 0o700); err != nil {
		return ManagedPackage{}, err
	}
	tmpBinary := filepath.Join(binDir, name)
	actual, err := copyExecutable(source, tmpBinary)
	if err != nil {
		return ManagedPackage{}, err
	}
	if actual != expected {
		return ManagedPackage{}, errors.New("extension package checksum mismatch")
	}
	manifest := ManagedPackage{ID: opts.ID, Version: opts.Version, Executable: destination, SHA256: actual, Root: root}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return ManagedPackage{}, err
	}
	if err := os.WriteFile(filepath.Join(tmp, "package.json"), raw, 0o600); err != nil {
		return ManagedPackage{}, err
	}
	if err := os.Rename(tmp, root); err != nil {
		return ManagedPackage{}, err
	}
	return manifest, nil
}

// InstalledManagedPackage returns the newest valid installed version.
func InstalledManagedPackage(id string) (ManagedPackage, bool) {
	base, err := existingManagedPackageBase(id)
	if err != nil {
		return ManagedPackage{}, false
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return ManagedPackage{}, false
	}
	versions := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() && packageVersionPattern.MatchString(entry.Name()) && !strings.HasPrefix(entry.Name(), ".") {
			versions = append(versions, entry.Name())
		}
	}
	slices.SortFunc(versions, comparePackageVersions)
	for i := len(versions) - 1; i >= 0; i-- {
		if installed, ok := loadManagedPackage(filepath.Join(base, versions[i]), id); ok {
			return installed, true
		}
	}
	return ManagedPackage{}, false
}

// RemoveManagedPackage removes only the exact registered package root. Source
// config and login state are separate explicit operations.
func RemoveManagedPackage(pkg ManagedPackage) error {
	base, err := existingManagedPackageBase(pkg.ID)
	if err != nil {
		return err
	}
	root := filepath.Clean(pkg.Root)
	rel, err := filepath.Rel(base, root)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel != pkg.Version {
		return errors.New("refusing package removal outside the managed version directory")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("refusing non-directory managed package root")
	}
	return os.RemoveAll(root)
}

// RemoveAllManagedPackages removes every valid installed version for one
// first-party package. An unexpected directory fails closed instead of turning
// uninstall into an arbitrary recursive delete.
func RemoveAllManagedPackages(id string) error {
	base, err := existingManagedPackageBase(id)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			return errors.New("managed package directory contains an unexpected entry; refusing uninstall")
		}
		root := filepath.Join(base, entry.Name())
		pkg, ok := loadManagedPackage(root, id)
		if !ok {
			return errors.New("managed package directory contains an invalid version; refusing uninstall")
		}
		if err := RemoveManagedPackage(pkg); err != nil {
			return err
		}
	}
	return os.Remove(base)
}

func managedPackagePath(id string) (string, error) {
	if _, ok := firstPartyPackages[id]; !ok {
		return "", errors.New("unknown first-party extension package")
	}
	root := golderhome.Dir()
	if root == "" {
		return "", errors.New("golder state root unavailable")
	}
	return filepath.Join(root, "extensions", "packages", id), nil
}

func existingManagedPackageBase(id string) (string, error) {
	base, err := managedPackagePath(id)
	if err != nil {
		return "", err
	}
	root := golderhome.Dir()
	for _, path := range []string{root, filepath.Join(root, "extensions"), filepath.Join(root, "extensions", "packages"), base} {
		info, err := os.Lstat(path)
		if err != nil {
			return "", err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("extension package directory is not a real private directory")
		}
	}
	return base, nil
}

func ensureManagedPackageBase(id string) (string, error) {
	root := golderhome.Dir()
	if root == "" {
		return "", errors.New("golder state root unavailable")
	}
	path := filepath.Clean(root)
	for _, part := range []string{"", "extensions", "packages", id} {
		if part != "" {
			path = filepath.Join(path, part)
		}
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			if err := os.Mkdir(path, 0o700); err != nil {
				return "", err
			}
			info, err = os.Lstat(path)
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("extension package directory is not a real private directory")
		}
		if err := os.Chmod(path, 0o700); err != nil {
			return "", err
		}
	}
	return path, nil
}

func checkedBundleExecutable(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("executable path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("executable must be a regular file, not a symlink")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return "", errors.New("executable is not marked executable")
	}
	return filepath.Clean(path), nil
}

func checkedExecutable(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("executable path must be absolute")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("executable must resolve to a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return "", errors.New("executable is not marked executable")
	}
	return filepath.Clean(resolved), nil
}

func copyExecutable(source, destination string) (string, error) {
	in, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(out, hash), in)
	if copyErr == nil {
		copyErr = out.Sync()
	}
	closeErr := out.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func loadManagedPackage(root, expectedID string) (ManagedPackage, bool) {
	raw, err := readLimited(filepath.Join(root, "package.json"), 64<<10)
	if err != nil {
		return ManagedPackage{}, false
	}
	var pkg ManagedPackage
	meta, registered := firstPartyPackages[expectedID]
	expectedExecutable := filepath.Join(root, "bin", executableName(meta.Binary))
	if json.Unmarshal(raw, &pkg) != nil || !registered || pkg.Root != root || pkg.ID != expectedID ||
		pkg.Version != filepath.Base(root) || pkg.Executable != expectedExecutable || !packageVersionPattern.MatchString(pkg.Version) {
		return ManagedPackage{}, false
	}
	path, err := checkedBundleExecutable(pkg.Executable)
	if err != nil || path != pkg.Executable {
		return ManagedPackage{}, false
	}
	f, err := os.Open(path)
	if err != nil {
		return ManagedPackage{}, false
	}
	hash := sha256.New()
	_, err = io.Copy(hash, f)
	_ = f.Close()
	if err != nil || hex.EncodeToString(hash.Sum(nil)) != pkg.SHA256 {
		return ManagedPackage{}, false
	}
	return pkg, true
}

func executableName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

func comparePackageVersions(a, b string) int {
	parse := func(value string) ([]int, bool) {
		core := strings.SplitN(strings.TrimPrefix(value, "v"), "-", 2)[0]
		parts := strings.Split(core, ".")
		out := make([]int, len(parts))
		for i, part := range parts {
			n, err := strconv.Atoi(part)
			if err != nil {
				return nil, false
			}
			out[i] = n
		}
		return out, true
	}
	left, leftOK := parse(a)
	right, rightOK := parse(b)
	if !leftOK || !rightOK {
		return strings.Compare(a, b)
	}
	for i := 0; i < max(len(left), len(right)); i++ {
		var x, y int
		if i < len(left) {
			x = left[i]
		}
		if i < len(right) {
			y = right[i]
		}
		if x < y {
			return -1
		}
		if x > y {
			return 1
		}
	}
	// A release sorts after its prerelease with the same numeric core.
	aPre, bPre := strings.Contains(a, "-"), strings.Contains(b, "-")
	if aPre != bPre {
		if aPre {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}

// LarkSourceOptions is the non-secret configuration collected by the GUI.
// AppSecretCredential names a value in .credentials.yaml; no secret is accepted.
type LarkSourceOptions struct {
	Package             ManagedPackage
	LarkCLI             string
	ProjectCLI          string
	AppID               string
	AppSecretCredential string
	Domains             []string
	ProjectHost         string
}

// LarkSourceConfig builds the fixed first-party source declaration. The only
// variable executable paths are explicit native CLI selections included in the
// fingerprint and sandbox capability list.
func LarkSourceConfig(opts LarkSourceOptions) (SourceConfig, error) {
	base, err := existingManagedPackageBase("lark")
	if err != nil {
		return SourceConfig{}, errors.New("managed golder-lark package is unavailable")
	}
	rel, err := filepath.Rel(base, opts.Package.Root)
	if err != nil || rel != opts.Package.Version || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return SourceConfig{}, errors.New("managed golder-lark package path is invalid")
	}
	installed, ok := loadManagedPackage(opts.Package.Root, "lark")
	if !ok || installed.Executable != opts.Package.Executable || installed.SHA256 != opts.Package.SHA256 {
		return SourceConfig{}, errors.New("managed golder-lark package is missing or changed")
	}
	larkCLI, err := checkedExecutable(opts.LarkCLI)
	if err != nil {
		return SourceConfig{}, fmt.Errorf("lark-cli: %w", err)
	}
	projectCLI := ""
	if strings.TrimSpace(opts.ProjectCLI) != "" {
		projectCLI, err = checkedExecutable(opts.ProjectCLI)
		if err != nil {
			return SourceConfig{}, fmt.Errorf("Project CLI: %w", err)
		}
	}
	appID := strings.TrimSpace(opts.AppID)
	credential := strings.TrimSpace(opts.AppSecretCredential)
	if !appIDPattern.MatchString(appID) || !credentialNamePattern.MatchString(credential) {
		return SourceConfig{}, errors.New("Lark application ID and named App Secret credential are required")
	}
	domains, err := normalizeLarkDomains(opts.Domains)
	if err != nil {
		return SourceConfig{}, err
	}
	host := strings.TrimSpace(opts.ProjectHost)
	if host == "" {
		host = "project.feishu.cn"
	}
	parsed, err := url.Parse("https://" + host)
	if err != nil || parsed.Host != host || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" ||
		net.ParseIP(parsed.Hostname()) != nil || !validPublicHostname(host) {
		return SourceConfig{}, errors.New("Project host must be a reviewed public hostname")
	}
	env := map[string]string{
		"LARK_MCP_CLI":        larkCLI,
		"LARK_MCP_IDENTITY":   "user",
		"LARK_MCP_APP_ID":     appID,
		"LARK_MCP_APP_SECRET": "${credential:" + credential + "}",
		"LARK_MCP_DOMAINS":    strings.Join(domains, ","),
		"MEEGLE_HOST":         host,
	}
	executables := []string{larkCLI}
	variants := map[string][]string{}
	if projectCLI != "" {
		env["LARK_MCP_PROJECT_CLI"] = projectCLI
		executables = append(executables, projectCLI)
		variants["project"] = []string{installed.Executable, "setup", "--events", "--service=project"}
	}
	setupOrigins := []string{"https://accounts.feishu.cn", "https://open.feishu.cn", "https://project.feishu.cn"}
	customOrigin := "https://" + host
	if !slices.Contains(setupOrigins, customOrigin) {
		setupOrigins = append(setupOrigins, customOrigin)
	}
	return SourceConfig{
		Enabled: false, Transport: "stdio", Command: installed.Executable,
		Network: true, StateDir: true, CallTimeout: "120s",
		Setup:         []string{installed.Executable, "setup", "--events", "--service=lark"},
		SetupVariants: variants,
		SetupOrigins:  setupOrigins,
		Executables:   executables, Env: env,
		ConfirmTools:   []string{"doc_write", "message_send", "ticket_update", "ticket_transition", "sheet_write", "sheet_structure", "base_record_write", "cli_call"},
		AggregateTools: []string{"cli_call"},
	}, nil
}

func normalizeLarkDomains(values []string) ([]string, error) {
	allowed := map[string]bool{"docs": true, "drive": true, "im": true, "sheets": true, "base": true}
	if len(values) == 0 {
		values = []string{"docs", "drive", "im", "sheets", "base"}
	}
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if !allowed[value] {
			return nil, fmt.Errorf("unsupported Lark service %q", value)
		}
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("select at least one Lark service")
	}
	return out, nil
}

func validPublicHostname(host string) bool {
	if len(host) > 253 || strings.ContainsAny(host, ":/\x00\r\n") {
		return false
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if !hostLabelPattern.MatchString(label) {
			return false
		}
	}
	return true
}
