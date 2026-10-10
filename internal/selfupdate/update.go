// This file implements golder's binary self-replacement for `golder update` (issue
// #466). Given the current build version it discovers the latest release (via
// version.go), downloads the matching goreleaser archive for the running
// GOOS/GOARCH, verifies its SHA256 against the release's checksums.txt, and
// atomically replaces the running executable.
//
// The archive naming mirrors .goreleaser.yaml and install.sh exactly, so this
// stays a single source of truth with the release tooling. Replacement is
// atomic: the new binary is written to a temp file in the target's directory
// and os.Rename'd over the current executable, so a failure mid-download never
// leaves a truncated binary in place.
package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bufio"
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
	"runtime"
	"strings"
	"sync/atomic"
	"time"
)

// checksumsFile is the goreleaser checksums artifact name (see .goreleaser.yaml).
const checksumsFile = "checksums.txt"

// mirrorEnv overrides the built-in mirror list: a whitespace-separated list of
// URL prefixes, or "off"/"none" to disable mirror fallback (GitHub only).
const mirrorEnv = "GOLDER_MIRROR"

// defaultMinRate is the average throughput (bytes/sec) a download attempt
// must keep once it has started delivering; below it the attempt is aborted
// and the next source is tried. Release archives are ~10 MB, so 50 KB/s
// still finishes in about 3.5 minutes, while the "connected but trickling
// at a few KB/s" sources common on mainland networks fail over within one
// window instead of crawling for half an hour. install.sh's curl flags
// (--speed-limit/--speed-time) must stay in sync with this value.
const defaultMinRate = 50 * 1024

// defaultRateWindow is both the measurement window for defaultMinRate and
// the time an attempt may deliver nothing at all (no first byte, or a silent
// stretch) before it is dropped. GitHub access from mainland China often
// connects and then stalls; without this the client's full timeout would be
// spent on a dead transfer before any fallback.
const defaultRateWindow = 15 * time.Second

// defaultDownloadTimeout is the wall-clock cap for one fetch (the HTTP
// client's total timeout). It is the backstop above the rate floor: a ~10 MB
// archive at the floor completes in about 3.5 minutes, so 5 minutes leaves
// headroom for a source that holds the floor without ever dropping below it.
const defaultDownloadTimeout = 5 * time.Minute

// defaultMirrorPrefixes are community-run GitHub download accelerators, tried
// in order after the direct download fails. A mirror URL is formed by
// prefixing the original GitHub URL (the common gh-proxy convention). Mirrors
// only accelerate the transfer: the archive is always verified against
// checksums.txt before it can replace the binary, and GOLDER_MIRROR=off
// disables them entirely.
var defaultMirrorPrefixes = []string{
	"https://ghfast.top/",
	"https://ghproxy.net/",
	"https://gh-proxy.com/",
	"https://gh.zwy.one/",
}

// mirrorPrefixes resolves the mirror list: GOLDER_MIRROR when set (a
// whitespace-separated prefix list, or off/none), else the built-in defaults.
func mirrorPrefixes() []string {
	raw := strings.TrimSpace(os.Getenv(mirrorEnv))
	switch raw {
	case "":
		return defaultMirrorPrefixes
	case "off", "none":
		return nil
	default:
		return strings.Fields(raw)
	}
}

// Updater performs a self-replacement. Its fields are seams for testing;
// NewUpdater fills them with production defaults.
type Updater struct {
	HTTPClient *http.Client
	// Repo is "owner/name"; ReleaseBaseURL overrides the download host in tests.
	Repo           string
	ReleaseBaseURL string // e.g. https://github.com/getan/golder/releases/download
	GOOS, GOARCH   string
	// ExecPath is the executable to replace; defaults to os.Executable().
	ExecPath string
	// Mirrors are URL prefixes tried, in order, after the direct GitHub
	// download fails (each applied as prefix+url). Empty means no fallback.
	// NewUpdater fills it from GOLDER_MIRROR or the built-in defaults; tests
	// set it explicitly.
	Mirrors []string
	// MinRateBytesPerSec aborts an attempt whose windowed average throughput
	// falls below this floor (see defaultMinRate); zero means the default.
	MinRateBytesPerSec int64
	// RateWindow is the measurement window for MinRateBytesPerSec, and also
	// the time an attempt may deliver no data before it is dropped; zero
	// means defaultRateWindow.
	RateWindow time.Duration
}

// Run performs `golder update` (self-update golder). It discovers the latest
// release, compares it to current, and replaces the running binary when a
// newer release exists. When current is a source build ("dev"), it cannot
// compare and proceeds to install the latest. Returns a process exit code.
func Run(ctx context.Context, current string, out, errOut io.Writer) int {
	tag, err := LatestTag(ctx, nil, Repo, current)
	if err != nil {
		fmt.Fprintf(errOut, "golder: failed to check for updates: %v\n", err)
		return 1
	}
	if avail, comparable := UpdateAvailable(current, tag); comparable && !avail {
		fmt.Fprintf(out, "already up to date at %s\n", current)
		return 0
	}
	u, err := NewUpdater()
	if err != nil {
		fmt.Fprintf(errOut, "golder: %v\n", err)
		return 1
	}
	if err := u.Apply(ctx, tag, out); err != nil {
		fmt.Fprintf(errOut, "golder: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "updated to %s\n", tag)
	return 0
}

// NewUpdater returns an Updater configured for the running process.
func NewUpdater() (*Updater, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("selfupdate: locate executable: %w", err)
	}
	// Resolve symlinks so we replace the real file, not a symlink.
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return &Updater{
		HTTPClient:     &http.Client{Timeout: defaultDownloadTimeout},
		Repo:           Repo,
		ReleaseBaseURL: "https://github.com/" + Repo + "/releases/download",
		GOOS:           runtime.GOOS,
		GOARCH:         runtime.GOARCH,
		ExecPath:       exe,
		Mirrors:        mirrorPrefixes(),
	}, nil
}

// archiveName builds the goreleaser archive filename for a release version
// (without leading "v") on the updater's platform. It mirrors the
// name_template and format_overrides in .goreleaser.yaml.
func (u *Updater) archiveName(versionNoV string) string {
	osName := map[string]string{"darwin": "Darwin", "linux": "Linux", "windows": "Windows"}[u.GOOS]
	if osName == "" {
		osName = u.GOOS
	}
	arch := map[string]string{"amd64": "x86_64", "386": "i386"}[u.GOARCH]
	if arch == "" {
		arch = u.GOARCH // arm64 and others pass through
	}
	ext := "tar.gz"
	if u.GOOS == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("golder_%s_%s_%s.%s", versionNoV, osName, arch, ext)
}

// binaryName is the executable name inside the archive.
func (u *Updater) binaryName() string {
	if u.GOOS == "windows" {
		return "golder.exe"
	}
	return "golder"
}

// Apply downloads the release identified by tag, verifies its checksum, and
// atomically replaces the target executable. tag is like "v0.4.0".
//
// The archive is accepted from the first source whose bytes match the trusted
// digest — a mirror serving a corrupt or stale copy simply fails over to the
// next source. The digest itself follows a trust ladder (see fetchChecksums):
// GitHub direct first, mirrors only when corroborated.
func (u *Updater) Apply(ctx context.Context, tag string, out io.Writer) error {
	versionNoV := strings.TrimPrefix(strings.TrimSpace(tag), "v")
	archive := u.archiveName(versionNoV)
	base := fmt.Sprintf("%s/%s", strings.TrimRight(u.ReleaseBaseURL, "/"), tag)
	archiveURL := base + "/" + archive

	fmt.Fprintf(out, "downloading %s ...\n", archive)

	want, err := u.fetchChecksums(ctx, base+"/"+checksumsFile, archive, out)
	if err != nil {
		return fmt.Errorf("selfupdate: download checksums: %w", err)
	}

	archiveBytes, source, err := u.fetch(ctx, archiveURL, archive, out, func(body []byte) error {
		got := sha256.Sum256(body)
		if hex.EncodeToString(got[:]) != want {
			return errors.New("sha256 mismatch")
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("selfupdate: download archive: %w", err)
	}
	if source != archiveURL {
		fmt.Fprintf(out, "  downloaded via %s\n", sourceHost(source))
	}

	binary, err := extractBinary(archiveBytes, u.binaryName(), u.GOOS == "windows")
	if err != nil {
		return err
	}
	if err := u.replace(binary); err != nil {
		return err
	}
	return nil
}

// fetch downloads original, trying the direct URL first and then each mirror,
// returning the first body accepted by ok (a nil ok accepts any 200 body).
// Failures are reported on out (when non-nil) so a fallback is visible rather
// than looking like a hang; name labels those notices (e.g. "checksums.txt").
// The returned string is the source that succeeded.
func (u *Updater) fetch(ctx context.Context, original, name string, out io.Writer, ok func([]byte) error) ([]byte, string, error) {
	sources := append([]string{original}, u.mirrorURLs(original)...)
	var lastErr error
	for i, src := range sources {
		if err := ctx.Err(); err != nil {
			if lastErr == nil {
				lastErr = err
			}
			break
		}
		body, err := u.download(ctx, src)
		if err == nil && ok != nil {
			err = ok(body)
		}
		if err == nil {
			return body, src, nil
		}
		lastErr = err
		if out != nil && i+1 < len(sources) {
			fmt.Fprintf(out, "  %s via %s failed (%v), trying %s ...\n",
				name, sourceHost(src), shortErr(err), sourceHost(sources[i+1]))
		}
	}
	return nil, "", lastErr
}

// fetchChecksums obtains checksums.txt and returns the expected sha256 for
// archive. GitHub direct is the trust anchor and is tried first. When it is
// unreachable the mirrors are cross-checked: their copies are accepted only
// when every responding source agrees on the digest for this archive, so a
// single mirror cannot vouch for an archive it poisoned. A lone mirror (a
// blocked network where just one answers) is accepted with an explicit
// warning — that is the tradeoff the mirrors exist for, now made visible —
// while any disagreement aborts as possible tampering.
func (u *Updater) fetchChecksums(ctx context.Context, original, archive string, out io.Writer) (string, error) {
	if body, err := u.download(ctx, original); err == nil {
		return checksumFor(body, archive)
	} else if out != nil {
		fmt.Fprintf(out, "  checksums.txt via %s failed (%v), cross-checking mirrors ...\n",
			sourceHost(original), shortErr(err))
	}

	murls := u.mirrorURLs(original)
	if len(murls) == 0 {
		return "", errors.New("GitHub direct fetch of checksums.txt failed and mirroring is disabled")
	}

	type candidate struct {
		src    string
		digest string
	}
	var cands []candidate
	for _, src := range murls {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		body, err := u.download(ctx, src)
		if err != nil {
			if out != nil {
				fmt.Fprintf(out, "  checksums.txt via %s failed (%v)\n", sourceHost(src), shortErr(err))
			}
			continue
		}
		want, err := checksumFor(body, archive)
		if err != nil {
			if out != nil {
				fmt.Fprintf(out, "  checksums.txt via %s has no entry for %s\n", sourceHost(src), archive)
			}
			continue
		}
		cands = append(cands, candidate{src: src, digest: want})
	}
	if len(cands) == 0 {
		return "", errors.New("GitHub direct fetch of checksums.txt failed and no mirror served a copy")
	}
	for _, c := range cands[1:] {
		if c.digest != cands[0].digest {
			return "", fmt.Errorf(
				"mirrors disagree on checksums.txt for %s: %s says %s, %s says %s — possible tampering, aborting (use GOLDER_MIRROR to pin sources)",
				archive, sourceHost(cands[0].src), shortDigest(cands[0].digest), sourceHost(c.src), shortDigest(c.digest))
		}
	}
	if out != nil {
		if len(cands) == 1 {
			fmt.Fprintf(out, "  warning: checksums.txt served only by %s (GitHub direct unreachable, nothing to cross-check)\n",
				sourceHost(cands[0].src))
		} else {
			fmt.Fprintf(out, "  checksums.txt via mirrors: %d independent sources agree\n", len(cands))
		}
	}
	return cands[0].digest, nil
}

// shortDigest abbreviates a sha256 for one-line notices.
func shortDigest(d string) string {
	if len(d) > 12 {
		return d[:12] + "…"
	}
	return d
}

// mirrorURLs applies each mirror prefix to an original GitHub URL.
func (u *Updater) mirrorURLs(original string) []string {
	if len(u.Mirrors) == 0 {
		return nil
	}
	urls := make([]string, 0, len(u.Mirrors))
	for _, m := range u.Mirrors {
		if m != "" {
			urls = append(urls, m+original)
		}
	}
	return urls
}

// sourceHost returns the host of a download source for user-facing notices,
// falling back to the raw string when it cannot be parsed.
func sourceHost(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return raw
}

// shortErr strips the request URL that net/http wraps into its errors, so a
// fallback notice stays one readable line.
func shortErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err
	}
	return err
}

// download fetches url and returns the full body. A non-200 status is an
// error. An attempt that goes silent for a full window, or that has started
// delivering but keeps averaging below the minimum rate, is aborted
// (cancelling the request context also cuts the in-flight body read) so the
// caller can move on to the next source. The rate clock starts at the first
// byte, so a slow handshake is charged to the silence rule rather than to a
// rate the source never had a chance to keep. When the response announces a
// Content-Length and it has been received in full, the rate check stands
// down: a short final window would otherwise charge a finished transfer's
// tail to the floor and abort it just before EOF.
func (u *Updater) download(ctx context.Context, url string) ([]byte, error) {
	window := u.RateWindow
	if window <= 0 {
		window = defaultRateWindow
	}
	minRate := u.MinRateBytesPerSec
	if minRate <= 0 {
		minRate = defaultMinRate
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := u.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	contentLength := resp.ContentLength

	// Watchdog: cancel the request on silence (no first byte, or no bytes at
	// all, for a full window) or on sustained low throughput (the windowed
	// average stayed under minRate). A separate goroutine plus atomics keeps
	// the checks out of the read loop and avoids timer-reset races.
	var firstByte, total atomic.Int64
	var slow atomic.Pointer[string]
	started := time.Now()
	done := make(chan struct{})
	go func() {
		interval := window / 4
		if interval < 25*time.Millisecond {
			interval = 25 * time.Millisecond
		}
		t := time.NewTicker(interval)
		defer t.Stop()
		var winStart time.Time
		var winBase int64
		complete := false
		for {
			select {
			case <-done:
				return
			case now := <-t.C:
				fb := firstByte.Load()
				if fb == 0 {
					if now.Sub(started) >= window {
						reason := fmt.Sprintf("no data for %s", window)
						slow.Store(&reason)
						cancel()
						return
					}
					continue
				}
				if complete {
					continue
				}
				if contentLength >= 0 && total.Load() >= contentLength {
					// All announced bytes are in; only EOF is pending. Do not
					// charge the tail of a finished transfer to the rate floor.
					complete = true
					continue
				}
				if winStart.IsZero() {
					winStart, winBase = time.Unix(0, fb), 0
				}
				elapsed := now.Sub(winStart)
				if elapsed < window {
					continue
				}
				got := total.Load() - winBase
				if rate := float64(got) / elapsed.Seconds(); rate < float64(minRate) {
					reason := fmt.Sprintf("too slow: %.1f KB/s over %s (min %d KB/s)",
						rate/1024, window, minRate/1024)
					slow.Store(&reason)
					cancel()
					return
				}
				winStart, winBase = now, total.Load()
			}
		}
	}()
	defer close(done)

	var buf bytes.Buffer
	chunk := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(chunk)
		if n > 0 {
			total.Add(int64(n))
			firstByte.CompareAndSwap(0, time.Now().UnixNano())
			buf.Write(chunk[:n])
		}
		if err == io.EOF {
			return buf.Bytes(), nil
		}
		if err != nil {
			if p := slow.Load(); p != nil {
				return nil, errors.New(*p)
			}
			return nil, err
		}
	}
}

// replace atomically swaps the target executable with newBin: it writes a temp
// file in the target's directory, sets it executable, and renames it over the
// target. Writing to the same directory keeps the rename atomic (same
// filesystem). A permission error on the directory yields an actionable message.
func (u *Updater) replace(newBin []byte) error {
	dir := filepath.Dir(u.ExecPath)
	tmp, err := os.CreateTemp(dir, ".golder-update-*")
	if err != nil {
		return fmt.Errorf("selfupdate: cannot write to %s: %w (try running with sudo, or install golder to a writable directory)", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after successful rename

	if _, err := tmp.Write(newBin); err != nil {
		tmp.Close()
		return fmt.Errorf("selfupdate: write new binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("selfupdate: close new binary: %w", err)
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return fmt.Errorf("selfupdate: chmod new binary: %w", err)
	}
	if err := os.Rename(tmpName, u.ExecPath); err != nil {
		return fmt.Errorf("selfupdate: replace %s: %w", u.ExecPath, err)
	}
	return nil
}

// checksumFor finds the hex SHA256 for archive in a goreleaser checksums.txt
// body (lines of "<hex>  <filename>").
func checksumFor(sums []byte, archive string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && fields[1] == archive {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("selfupdate: %s not found in checksums.txt", archive)
}

// extractBinary pulls the named binary out of an archive (tar.gz, or zip when
// isZip). It returns the binary bytes.
func extractBinary(archive []byte, name string, isZip bool) ([]byte, error) {
	if isZip {
		return extractFromZip(archive, name)
	}
	return extractFromTarGz(archive, name)
}

func extractFromTarGz(archive []byte, name string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("selfupdate: gzip reader: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("selfupdate: read tar: %w", err)
		}
		if filepath.Base(hdr.Name) == name && hdr.Typeflag == tar.TypeReg {
			return io.ReadAll(tr)
		}
	}
	return nil, fmt.Errorf("selfupdate: %s not found in archive", name)
}

func extractFromZip(archive []byte, name string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, fmt.Errorf("selfupdate: zip reader: %w", err)
	}
	for _, f := range zr.File {
		if filepath.Base(f.Name) == name {
			rc, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("selfupdate: open %s in zip: %w", name, err)
			}
			defer rc.Close()
			return io.ReadAll(rc)
		}
	}
	return nil, fmt.Errorf("selfupdate: %s not found in archive", name)
}
