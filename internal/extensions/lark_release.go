package extensions

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	larkversion "github.com/getan/golder/extension/plugins/lark/version"
)

var larkReleaseVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

// LarkRelease identifies the exact first-party helper package the operator is
// asked to approve. Discovery does not download or execute anything.
type LarkRelease struct {
	Version     string
	Archive     string
	ArchiveURL  string
	ChecksumURL string
}

func larkRelease(version, goos, goarch, base string) (LarkRelease, error) {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	if !larkReleaseVersion.MatchString(version) {
		return LarkRelease{}, errors.New("a stable Golder release is required to install the matching Lark helper")
	}
	if (goos != "darwin" && goos != "linux") || (goarch != "amd64" && goarch != "arm64") {
		return LarkRelease{}, errors.New("the matching Lark helper is unavailable on this platform")
	}
	// Artifacts live in the host release; their package version is independent.
	archive := fmt.Sprintf("golder-lark_%s_%s_%s.tar.gz", version, goos, goarch)
	release := strings.TrimRight(base, "/") + "/v" + version
	return LarkRelease{Version: larkversion.Version, Archive: archive, ArchiveURL: release + "/" + archive, ChecksumURL: release + "/checksums.txt"}, nil
}

// LarkReleaseFor reports official URLs for a Golder release and the independent
// Lark version contained in that release.
func LarkReleaseFor(version string) (LarkRelease, error) {
	return larkRelease(version, runtime.GOOS, runtime.GOARCH, "https://github.com/getan/golder/releases/download")
}

// InstallLarkRelease downloads the exact matching first-party release after an
// explicit operator decision and verifies the archive before installing it.
func InstallLarkRelease(ctx context.Context, version string) (ManagedPackage, error) {
	release, err := LarkReleaseFor(version)
	if err != nil {
		return ManagedPackage{}, err
	}
	client := &http.Client{Timeout: 5 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 || req.URL.Scheme != "https" || !trustedReleaseHost(req.URL.Hostname()) {
			return errors.New("untrusted release redirect")
		}
		return nil
	}}
	return installLarkRelease(ctx, release, client)
}

func trustedReleaseHost(host string) bool {
	host = strings.ToLower(host)
	return host == "github.com" || host == "objects.githubusercontent.com" || host == "release-assets.githubusercontent.com" || host == "github-releases.githubusercontent.com"
}

func releaseDownload(ctx context.Context, client *http.Client, rawURL string, max int64) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || u.User != nil {
		return nil, errors.New("invalid Lark release URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, errors.New("cannot prepare Lark release download")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("Lark release download failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Lark release server returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil || int64(len(body)) > max {
		return nil, errors.New("Lark release download was incomplete or too large")
	}
	return body, nil
}

func installLarkRelease(ctx context.Context, release LarkRelease, client *http.Client) (ManagedPackage, error) {
	checksums, err := releaseDownload(ctx, client, release.ChecksumURL, 2<<20)
	if err != nil {
		return ManagedPackage{}, err
	}
	want := ""
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == release.Archive {
			if want != "" {
				return ManagedPackage{}, errors.New("duplicate Lark release checksum")
			}
			want = strings.ToLower(fields[0])
		}
	}
	if len(want) != sha256.Size*2 {
		return ManagedPackage{}, errors.New("matching Lark release checksum was not found")
	}
	if _, err := hex.DecodeString(want); err != nil {
		return ManagedPackage{}, errors.New("invalid Lark release checksum")
	}
	archive, err := releaseDownload(ctx, client, release.ArchiveURL, 128<<20)
	if err != nil {
		return ManagedPackage{}, err
	}
	digest := sha256.Sum256(archive)
	if hex.EncodeToString(digest[:]) != want {
		return ManagedPackage{}, errors.New("Lark release archive checksum mismatch")
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return ManagedPackage{}, errors.New("invalid Lark release archive")
	}
	defer gz.Close()
	expanded := &io.LimitedReader{R: gz, N: (128 << 20) + 1}
	reader := tar.NewReader(expanded)
	var binary []byte
	for {
		hdr, err := reader.Next()
		if expanded.N <= 0 {
			return ManagedPackage{}, errors.New("Lark release archive expands beyond the size limit")
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return ManagedPackage{}, errors.New("invalid Lark release archive")
		}
		if strings.TrimPrefix(hdr.Name, "./") != "golder-lark" {
			continue
		}
		if binary != nil || hdr.Typeflag != tar.TypeReg || hdr.Size <= 0 || hdr.Size > 96<<20 {
			return ManagedPackage{}, errors.New("invalid Lark helper in release archive")
		}
		binary, err = io.ReadAll(io.LimitReader(reader, (96<<20)+1))
		if err != nil || len(binary) == 0 || len(binary) > 96<<20 {
			return ManagedPackage{}, errors.New("cannot read Lark helper from release archive")
		}
	}
	if len(binary) == 0 {
		return ManagedPackage{}, errors.New("Lark helper was not found in the release archive")
	}
	if err := ctx.Err(); err != nil {
		return ManagedPackage{}, err
	}
	tmp, err := os.MkdirTemp("", "golder-lark-install-")
	if err != nil {
		return ManagedPackage{}, errors.New("cannot create temporary Lark installer")
	}
	defer os.RemoveAll(tmp)
	source := filepath.Join(tmp, "golder-lark")
	if err := os.WriteFile(source, binary, 0o700); err != nil {
		return ManagedPackage{}, errors.New("cannot stage the Lark helper")
	}
	binaryDigest := sha256.Sum256(binary)
	return InstallManagedPackage(PackageInstallOptions{ID: "lark", Version: release.Version, Source: source, SHA256: hex.EncodeToString(binaryDigest[:])})
}
