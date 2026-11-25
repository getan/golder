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
	"sync/atomic"
	"time"

	larkversion "github.com/getan/golder/extension/plugins/lark/version"
	"github.com/getan/golder/internal/githubmirror"
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

// LarkDownloadProgress is one downloader progress sample. Total is zero when
// the server does not announce a Content-Length.
type LarkDownloadProgress struct {
	Stage      string
	Source     string
	Downloaded int64
	Total      int64
}

var (
	larkSilenceWindow = 15 * time.Second
	larkMinRate       = float64(50 * 1024)
)

func larkRelease(version, goos, goarch, base string) (LarkRelease, error) {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	if !larkReleaseVersion.MatchString(version) {
		return LarkRelease{}, errors.New("a stable Golder release is required to install the matching Lark helper")
	}
	if (goos != "darwin" && goos != "linux") || (goarch != "amd64" && goarch != "arm64") {
		return LarkRelease{}, errors.New("the matching Lark helper is unavailable on this platform")
	}
	// Artifacts live in the host release; their package version is independent.
	// The asset name carries both versions so the download list shows which
	// helper a host release contains; .goreleaser.yaml uses the same template.
	archive := fmt.Sprintf("golder-lark_%s_helper%s_%s_%s.tar.gz", version, larkversion.Version, goos, goarch)
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
	return InstallLarkReleaseWithProgress(ctx, version, nil)
}

// InstallLarkReleaseWithProgress is InstallLarkRelease with byte progress.
// Progress callbacks run on the download goroutine and must not block.
func InstallLarkReleaseWithProgress(ctx context.Context, version string, progress func(LarkDownloadProgress)) (ManagedPackage, error) {
	release, err := LarkReleaseFor(version)
	if err != nil {
		return ManagedPackage{}, err
	}
	client := &http.Client{Timeout: 5 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 || req.URL.Scheme != "https" || req.URL.User != nil {
			return errors.New("untrusted release redirect")
		}
		return nil
	}}
	return installLarkReleaseWithProgress(ctx, release, client, progress)
}

func releaseDownload(ctx context.Context, client *http.Client, rawURL string, max int64) ([]byte, error) {
	return releaseDownloadProgress(ctx, client, rawURL, max, "", nil)
}

func releaseDownloadProgress(ctx context.Context, client *http.Client, rawURL string, max int64, stage string, progress func(LarkDownloadProgress)) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || u.User != nil {
		return nil, errors.New("invalid Lark release URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, errors.New("cannot prepare Lark release download")
	}
	dlCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	req = req.WithContext(dlCtx)
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("Lark release download failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Lark release server returned HTTP %d", resp.StatusCode)
	}
	source, total := u.Hostname(), resp.ContentLength
	if total < 0 {
		total = 0
	}
	start := time.Now()
	var downloaded, lastData atomic.Int64
	lastData.Store(start.UnixNano())
	var slow atomic.Pointer[string]
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case now := <-ticker.C:
				if now.Sub(time.Unix(0, lastData.Load())) >= larkSilenceWindow {
					reason := "no data for " + larkSilenceWindow.String()
					slow.Store(&reason)
					cancel()
					return
				}
				elapsed := now.Sub(start)
				if downloaded.Load() > 0 && elapsed >= larkSilenceWindow {
					rate := float64(downloaded.Load()) / elapsed.Seconds()
					if rate < larkMinRate {
						reason := fmt.Sprintf("download too slow (%.0f KB/s)", rate/1024)
						slow.Store(&reason)
						cancel()
						return
					}
				}
			}
		}
	}()
	var body bytes.Buffer
	buf := make([]byte, 32<<10)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if int64(body.Len())+int64(n) > max {
				return nil, errors.New("Lark release download was incomplete or too large")
			}
			_, _ = body.Write(buf[:n])
			downloaded.Add(int64(n))
			lastData.Store(time.Now().UnixNano())
			if progress != nil {
				progress(LarkDownloadProgress{Stage: stage, Source: source, Downloaded: downloaded.Load(), Total: total})
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			if p := slow.Load(); p != nil {
				return nil, errors.New("Lark release download failed: " + *p)
			}
			if dlCtx.Err() != nil {
				return nil, dlCtx.Err()
			}
			return nil, errors.New("Lark release download was incomplete")
		}
	}
	if total > 0 && int64(body.Len()) != total {
		return nil, errors.New("Lark release download was incomplete")
	}
	return body.Bytes(), nil
}

func larkChecksumFor(checksums []byte, archive string) (string, error) {
	want := ""
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == archive {
			if want != "" {
				return "", errors.New("duplicate Lark release checksum")
			}
			want = strings.ToLower(fields[0])
		}
	}
	if len(want) != sha256.Size*2 {
		return "", errors.New("matching Lark release checksum was not found")
	}
	if _, err := hex.DecodeString(want); err != nil {
		return "", errors.New("invalid Lark release checksum")
	}
	return want, nil
}

func larkDownloadAttempt(ctx context.Context, client *http.Client, rawURL string, max int64, timeout time.Duration, stage string, progress func(LarkDownloadProgress)) ([]byte, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	return releaseDownloadProgress(ctx, client, rawURL, max, stage, progress)
}

// fetchLarkChecksum tries GitHub direct first (the trust anchor). If it is
// unreachable, mirror copies are cross-checked: they must all agree on the
// archive digest, so one mirror cannot vouch for bytes it poisoned. A single
// reachable mirror is accepted, matching install.sh's documented tradeoff.
func fetchLarkChecksum(ctx context.Context, client *http.Client, release LarkRelease, progress func(LarkDownloadProgress)) (string, error) {
	sources := githubmirror.Apply(release.ChecksumURL, githubmirror.Prefixes())
	directErr := error(nil)
	if body, err := larkDownloadAttempt(ctx, client, sources[0], 2<<20, 45*time.Second, "checksums", progress); err == nil {
		if want, err := larkChecksumFor(body, release.Archive); err == nil {
			return want, nil
		} else {
			directErr = err
		}
	} else {
		directErr = err
	}
	var candidates []string
	for _, src := range sources[1:] {
		body, err := larkDownloadAttempt(ctx, client, src, 2<<20, 45*time.Second, "checksums", progress)
		if err != nil {
			continue
		}
		want, err := larkChecksumFor(body, release.Archive)
		if err != nil {
			continue
		}
		candidates = append(candidates, want)
	}
	if len(candidates) == 0 {
		if directErr == nil {
			directErr = errors.New("no mirror served a matching checksum")
		}
		return "", fmt.Errorf("Lark release checksum unavailable from GitHub or mirrors: %w", directErr)
	}
	for _, digest := range candidates[1:] {
		if digest != candidates[0] {
			return "", fmt.Errorf("Lark release checksum mirrors disagree for %s; possible tampering", release.Archive)
		}
	}
	return candidates[0], nil
}

// fetchLarkArchive tries direct GitHub, then each configured mirror. The
// checksum is already trusted by fetchLarkChecksum, so a mirror serving a
// stale or corrupt archive simply fails over.
func fetchLarkArchive(ctx context.Context, client *http.Client, release LarkRelease, want string, progress func(LarkDownloadProgress)) ([]byte, error) {
	var last error
	for _, src := range githubmirror.Apply(release.ArchiveURL, githubmirror.Prefixes()) {
		archive, err := larkDownloadAttempt(ctx, client, src, 128<<20, 90*time.Second, "archive", progress)
		if err != nil {
			last = err
			continue
		}
		digest := sha256.Sum256(archive)
		if hex.EncodeToString(digest[:]) != want {
			last = errors.New("Lark release archive checksum mismatch")
			continue
		}
		return archive, nil
	}
	if last == nil {
		last = errors.New("Lark release download failed")
	}
	return nil, last
}

func installLarkRelease(ctx context.Context, release LarkRelease, client *http.Client) (ManagedPackage, error) {
	return installLarkReleaseWithProgress(ctx, release, client, nil)
}

func installLarkReleaseWithProgress(ctx context.Context, release LarkRelease, client *http.Client, progress func(LarkDownloadProgress)) (ManagedPackage, error) {
	want, err := fetchLarkChecksum(ctx, client, release, progress)
	if err != nil {
		return ManagedPackage{}, err
	}
	archive, err := fetchLarkArchive(ctx, client, release, want, progress)
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
