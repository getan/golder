package extensions

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/getan/golder/internal/golderhome"
	"github.com/getan/golder/internal/privatepath"
)

type OfficialCLIKind string

const (
	OfficialLarkCLI    OfficialCLIKind = "lark-cli"
	OfficialProjectCLI OfficialCLIKind = "meegle"

	maxOfficialCLIArchive = 128 << 20
	maxOfficialCLIBinary  = 96 << 20
)

type OfficialCLI struct {
	Kind       OfficialCLIKind
	Version    string
	Executable string
}

type OfficialCLIInstallSpec struct {
	Version     string
	ArchiveURL  string
	SHA256      string
	SHA512      string
	ArchivePath string
	BinaryName  string
}

type OfficialCLIInstaller struct {
	Client *http.Client
	Root   string
	GOOS   string
	GOARCH string
	Specs  map[OfficialCLIKind]OfficialCLIInstallSpec
	// Progress is called synchronously on the installing goroutine. Callers
	// dispatch UI updates themselves; callbacks must not block the download.
	Progress            func(OfficialCLIInstallProgress)
	AllowHTTP           bool // tests only
	SkipCommonDiscovery bool // tests only
}

func NewOfficialCLIInstaller() *OfficialCLIInstaller {
	goos, goarch := runtime.GOOS, runtime.GOARCH
	root := ""
	if home := golderhome.Dir(); home != "" {
		root = filepath.Join(home, "extensions", "official-clis")
	}
	return &OfficialCLIInstaller{
		Client: newOfficialCLIClient(), Root: root,
		GOOS: goos, GOARCH: goarch, Specs: officialCLISpecs(goos, goarch),
	}
}

func officialCLISpecs(goos, goarch string) map[OfficialCLIKind]OfficialCLIInstallSpec {
	const larkVersion = "1.0.99"
	platform, arch := goos, goarch
	if goos == "windows" {
		platform = "windows"
	}
	larkName := fmt.Sprintf("lark-cli-%s-%s-%s.tar.gz", larkVersion, platform, arch)
	larkBinary := "lark-cli"
	if goos == "windows" {
		larkName = fmt.Sprintf("lark-cli-%s-windows-%s.zip", larkVersion, arch)
		larkBinary += ".exe"
	}
	larkSums := map[string]string{
		"darwin/amd64":  "31f900409082728761346a94b7b81038c78b302883b69c36b849b23d843d7de1",
		"darwin/arm64":  "f24f4106d1b0b1a4ee9a24ec0953dee6e518476bbe3d9758f78a732477525b21",
		"linux/amd64":   "fd29f51030dde6919b6ca9173bc87852d3846f253d198022ea2d9ee63527b945",
		"linux/arm64":   "2837145e72922d558538494d0da30ac6c509681fc0791f8818f4ddb87f5125b4",
		"windows/amd64": "1b43cb63ff533ee067db45d93a862074d147df5a0d05f95c2d2a7c70c3f97375",
		"windows/arm64": "6ce96dd8559ef7e0a2b4e228b81a955e3df71e6e19f2d2070b65df232b4ae8a4",
	}
	meeglePlatform, meegleArch := goos, goarch
	if goos == "windows" {
		meeglePlatform = "win32"
	}
	if goarch == "amd64" {
		meegleArch = "x64"
	}
	meegleBinary := "meegle-" + meeglePlatform + "-" + meegleArch
	installedMeegleName := "meegle"
	if goos == "windows" {
		meegleBinary += ".exe"
		installedMeegleName += ".exe"
	}
	return map[OfficialCLIKind]OfficialCLIInstallSpec{
		OfficialLarkCLI: {
			Version: larkVersion, ArchiveURL: "https://github.com/larksuite/cli/releases/download/v" + larkVersion + "/" + larkName,
			SHA256: larkSums[goos+"/"+goarch], ArchivePath: larkBinary, BinaryName: larkBinary,
		},
		OfficialProjectCLI: {
			Version: "1.0.27", ArchiveURL: "https://registry.npmjs.org/@lark-project/meegle/-/meegle-1.0.27.tgz",
			SHA512:      "UQdiVPbCTPDQeFBE5vGHPWBiI53n2HUb6EXlF+QWXWSeITNUVcZGd4Ogx4VO1Tky1Zb9kaWGPecwbCFwieXLqA==",
			ArchivePath: "package/bin/" + meegleBinary, BinaryName: installedMeegleName,
		},
	}
}

func (i *OfficialCLIInstaller) Install(ctx context.Context, kind OfficialCLIKind) (OfficialCLI, error) {
	if i == nil {
		return OfficialCLI{}, errors.New("official CLI installer is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return OfficialCLI{}, err
	}
	progress := OfficialCLIInstallProgress{Kind: kind}
	report := func(phase string) {
		progress.Phase = phase
		if i.Progress != nil {
			i.Progress(progress)
		}
	}
	report("checking")
	spec, ok := i.Specs[kind]
	if !ok || spec.Version == "" || spec.ArchiveURL == "" || spec.ArchivePath == "" || spec.BinaryName == "" {
		return OfficialCLI{}, fmt.Errorf("official %s binary is unavailable for %s/%s", kind, i.GOOS, i.GOARCH)
	}
	root := strings.TrimSpace(i.Root)
	if root == "" {
		return OfficialCLI{}, errors.New("Golder state root is unavailable")
	}
	targetRoot := filepath.Join(root, string(kind), spec.Version)
	target := filepath.Join(targetRoot, spec.BinaryName)
	if valid, _ := validateNativeCLI(target); valid {
		report("complete")
		return OfficialCLI{Kind: kind, Version: spec.Version, Executable: target}, nil
	}
	if _, err := os.Lstat(targetRoot); err == nil {
		return OfficialCLI{}, errors.New("official CLI destination exists but is invalid; remove it explicitly before retrying")
	} else if !os.IsNotExist(err) {
		return OfficialCLI{}, err
	}
	parsed, err := url.Parse(spec.ArchiveURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && !(i.AllowHTTP && parsed.Scheme == "http")) {
		return OfficialCLI{}, errors.New("official CLI download URL is invalid")
	}
	report("connecting")
	archive, err := fetchOfficialCLI(ctx, i.Client, spec.ArchiveURL, func(done, total int64) {
		progress.Downloaded, progress.Total = done, total
		report("downloading")
	})
	if err != nil {
		return OfficialCLI{}, err
	}
	if err := ctx.Err(); err != nil {
		return OfficialCLI{}, err
	}
	report("verifying")
	if err := verifyOfficialCLIArchive(archive, spec); err != nil {
		return OfficialCLI{}, err
	}
	report("extracting")
	binary, err := extractOfficialCLIBinary(archive, spec.ArchiveURL, spec.ArchivePath)
	if err != nil {
		return OfficialCLI{}, err
	}
	if !nativeBinary(binary) {
		return OfficialCLI{}, errors.New("official CLI archive did not contain a native executable")
	}
	if err := ctx.Err(); err != nil {
		return OfficialCLI{}, err
	}
	report("installing")
	kindRoot := filepath.Join(root, string(kind))
	if err := privatepath.EnsureDir(kindRoot); err != nil {
		return OfficialCLI{}, err
	}
	tmp, err := os.MkdirTemp(kindRoot, ".install-*")
	if err != nil {
		return OfficialCLI{}, err
	}
	defer os.RemoveAll(tmp)
	if err := os.Chmod(tmp, 0o700); err != nil {
		return OfficialCLI{}, err
	}
	tmpBinary := filepath.Join(tmp, spec.BinaryName)
	if err := os.WriteFile(tmpBinary, binary, 0o700); err != nil {
		return OfficialCLI{}, err
	}
	manifest, _ := json.MarshalIndent(map[string]string{"kind": string(kind), "version": spec.Version, "source": spec.ArchiveURL}, "", "  ")
	if err := os.WriteFile(filepath.Join(tmp, "install.json"), manifest, 0o600); err != nil {
		return OfficialCLI{}, err
	}
	if err := ctx.Err(); err != nil {
		return OfficialCLI{}, err
	}
	if err := os.Rename(tmp, targetRoot); err != nil {
		return OfficialCLI{}, err
	}
	report("complete")
	return OfficialCLI{Kind: kind, Version: spec.Version, Executable: target}, nil
}

func DiscoverOfficialCLI(kind OfficialCLIKind, preferred string) (OfficialCLI, bool) {
	installer := NewOfficialCLIInstaller()
	return installer.Discover(kind, preferred)
}

func (i *OfficialCLIInstaller) Discover(kind OfficialCLIKind, preferred string) (OfficialCLI, bool) {
	var candidates []string
	if strings.TrimSpace(preferred) != "" {
		candidates = append(candidates, preferred)
	}
	candidates = append(candidates, i.managedCandidates(kind)...)
	name := string(kind)
	if kind == OfficialProjectCLI {
		name = "meegle"
	}
	if path, err := exec.LookPath(name); err == nil {
		candidates = append(candidates, path)
	}
	if !i.SkipCommonDiscovery {
		candidates = append(candidates, commonOfficialCLICandidates(kind, i.GOOS, i.GOARCH)...)
	}
	seen := map[string]bool{}
	for _, candidate := range candidates {
		absolute, err := filepath.Abs(strings.TrimSpace(candidate))
		if err != nil || seen[absolute] {
			continue
		}
		seen[absolute] = true
		if valid, resolved := validateNativeCLI(absolute); valid {
			return OfficialCLI{Kind: kind, Executable: resolved}, true
		}
	}
	return OfficialCLI{}, false
}

func (i *OfficialCLIInstaller) managedCandidates(kind OfficialCLIKind) []string {
	spec, ok := i.Specs[kind]
	if !ok || strings.TrimSpace(i.Root) == "" {
		return nil
	}
	dir := filepath.Join(i.Root, string(kind))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	versions := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() && packageVersionPattern.MatchString(entry.Name()) && !strings.HasPrefix(entry.Name(), ".") {
			versions = append(versions, entry.Name())
		}
	}
	sort.Slice(versions, func(a, b int) bool { return comparePackageVersions(versions[a], versions[b]) > 0 })
	if len(versions) > 16 {
		versions = versions[:16]
	}
	paths := make([]string, 0, len(versions)+1)
	paths = append(paths, filepath.Join(dir, spec.Version, spec.BinaryName))
	for _, version := range versions {
		path := filepath.Join(dir, version, spec.BinaryName)
		if path != paths[0] {
			paths = append(paths, path)
		}
	}
	return paths
}

func commonOfficialCLICandidates(kind OfficialCLIKind, goos, goarch string) []string {
	home, _ := os.UserHomeDir()
	cwd, _ := os.Getwd()
	baseName := "lark-cli"
	packageName := "@larksuite/cli"
	packageBinary := "lark-cli"
	if kind == OfficialProjectCLI {
		baseName = "meegle"
		packageName = "@lark-project/meegle"
		platform, arch := goos, goarch
		if goos == "windows" {
			platform = "win32"
		}
		if goarch == "amd64" {
			arch = "x64"
		}
		packageBinary = "meegle-" + platform + "-" + arch
	}
	if goos == "windows" {
		baseName += ".exe"
		packageBinary += ".exe"
	}
	var paths []string
	if cwd != "" {
		paths = append(paths, filepath.Join(cwd, "node_modules", packageName, "bin", packageBinary))
	}
	paths = append(paths,
		filepath.Join("/opt/homebrew/bin", baseName),
		filepath.Join("/usr/local/bin", baseName),
	)
	var patterns []string
	if home != "" {
		paths = append(paths,
			filepath.Join(home, ".local", "lib", "golder-lark", baseName),
			filepath.Join(home, ".local", "bin", baseName),
		)
		patterns = append(patterns,
			filepath.Join(home, "Library", "pnpm", "global", "*", "node_modules", packageName, "bin", packageBinary),
			filepath.Join(home, ".local", "share", "pnpm", "global", "*", "node_modules", packageName, "bin", packageBinary),
			filepath.Join(home, ".npm-global", "lib", "node_modules", packageName, "bin", packageBinary),
		)
	}
	patterns = append(patterns,
		filepath.Join("/opt/homebrew/lib/node_modules", packageName, "bin", packageBinary),
		filepath.Join("/usr/local/lib/node_modules", packageName, "bin", packageBinary),
	)
	for _, pattern := range patterns {
		matches, _ := filepath.Glob(pattern)
		sort.Strings(matches)
		if len(matches) > 8 {
			matches = matches[:8]
		}
		paths = append(paths, matches...)
	}
	return paths
}

func validateNativeCLI(path string) (bool, string) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false, ""
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0) || info.Size() > maxOfficialCLIBinary {
		return false, ""
	}
	f, err := os.Open(resolved)
	if err != nil {
		return false, ""
	}
	defer f.Close()
	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil || !nativeBinary(magic[:]) {
		return false, ""
	}
	return true, resolved
}

func nativeBinary(data []byte) bool {
	if len(data) < 4 {
		return false
	}
	return bytes.Equal(data[:4], []byte("\x7fELF")) || bytes.Equal(data[:2], []byte("MZ")) ||
		bytes.Equal(data[:4], []byte{0xfe, 0xed, 0xfa, 0xcf}) || bytes.Equal(data[:4], []byte{0xcf, 0xfa, 0xed, 0xfe}) ||
		bytes.Equal(data[:4], []byte{0xfe, 0xed, 0xfa, 0xce}) || bytes.Equal(data[:4], []byte{0xce, 0xfa, 0xed, 0xfe}) ||
		bytes.Equal(data[:4], []byte{0xca, 0xfe, 0xba, 0xbe})
}

func newOfficialCLIClient() *http.Client {
	return &http.Client{
		Timeout: 3 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 4 || !allowedOfficialCLIHost(req.URL.Hostname()) {
				return errors.New("official CLI download redirect was refused")
			}
			return nil
		},
	}
}

func allowedOfficialCLIHost(host string) bool {
	host = strings.ToLower(host)
	return host == "github.com" || host == "release-assets.githubusercontent.com" || host == "objects.githubusercontent.com" || host == "registry.npmjs.org"
}

func fetchOfficialCLI(ctx context.Context, client *http.Client, rawURL string, progress func(int64, int64)) ([]byte, error) {
	if client == nil {
		client = newOfficialCLIClient()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "golder-gui-official-cli-installer")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download official CLI: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download official CLI: HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxOfficialCLIArchive {
		return nil, errors.New("official CLI archive exceeds the size limit")
	}
	total := max(resp.ContentLength, 0)
	if progress != nil {
		progress(0, total)
	}
	reader := &officialCLIProgressReader{reader: resp.Body, total: total, report: progress}
	body, err := io.ReadAll(io.LimitReader(reader, maxOfficialCLIArchive+1))
	if err != nil {
		return nil, fmt.Errorf("download official CLI: %w", err)
	}
	if len(body) > maxOfficialCLIArchive {
		return nil, errors.New("official CLI archive exceeds the size limit")
	}
	if total > 0 && int64(len(body)) != total {
		return nil, errors.New("official CLI download ended before the advertised size was received")
	}
	return body, nil
}

func verifyOfficialCLIArchive(archive []byte, spec OfficialCLIInstallSpec) error {
	switch {
	case spec.SHA256 != "":
		sum := sha256.Sum256(archive)
		if !strings.EqualFold(hex.EncodeToString(sum[:]), spec.SHA256) {
			return errors.New("official CLI SHA-256 checksum mismatch")
		}
	case spec.SHA512 != "":
		want, err := base64.StdEncoding.DecodeString(spec.SHA512)
		if err != nil {
			return errors.New("official CLI SHA-512 integrity is invalid")
		}
		sum := sha512.Sum512(archive)
		if !bytes.Equal(sum[:], want) {
			return errors.New("official CLI SHA-512 integrity mismatch")
		}
	default:
		return errors.New("official CLI archive has no pinned integrity")
	}
	return nil
}

func extractOfficialCLIBinary(archive []byte, rawURL, wanted string) ([]byte, error) {
	if strings.HasSuffix(rawURL, ".zip") {
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, err
		}
		for _, file := range zr.File {
			if filepath.ToSlash(file.Name) != wanted || file.UncompressedSize64 > maxOfficialCLIBinary {
				continue
			}
			reader, err := file.Open()
			if err != nil {
				return nil, err
			}
			data, err := io.ReadAll(io.LimitReader(reader, maxOfficialCLIBinary+1))
			_ = reader.Close()
			if err != nil || len(data) > maxOfficialCLIBinary {
				return nil, errors.New("official CLI binary exceeds the size limit")
			}
			return data, nil
		}
		return nil, errors.New("official CLI archive does not contain the expected binary")
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if filepath.ToSlash(header.Name) != wanted || header.Typeflag != tar.TypeReg {
			continue
		}
		if header.Size < 0 || header.Size > maxOfficialCLIBinary {
			return nil, errors.New("official CLI binary exceeds the size limit")
		}
		data, err := io.ReadAll(io.LimitReader(tr, maxOfficialCLIBinary+1))
		if err != nil || len(data) > maxOfficialCLIBinary {
			return nil, errors.New("official CLI binary exceeds the size limit")
		}
		return data, nil
	}
	return nil, errors.New("official CLI archive does not contain the expected binary")
}
