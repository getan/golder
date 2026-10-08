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

// defaultStallTimeout is how long a download attempt may deliver no data at
// all before it is aborted and the next source is tried. GitHub access from
// mainland China often connects and then stalls; without this the client's
// full timeout would be spent on a dead transfer before any fallback.
const defaultStallTimeout = 15 * time.Second

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
	// StallTimeout aborts an attempt that delivers no data for this long;
	// zero means defaultStallTimeout.
	StallTimeout time.Duration
}

// Run performs `golder update` (self-update golder). It discovers the latest
// release, compares it to current, and replaces the running binary when a
// newer release exists. When current is a source build ("dev"), it cannot
// compare and proceeds to install the latest. Returns a process exit code.
func Run(ctx context.Context, current string, out, errOut io.Writer) int {
	tag, err := LatestTag(ctx, nil, Repo)
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
		HTTPClient:     &http.Client{Timeout: 60 * time.Second},
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
// Downloads try the direct GitHub URL first and then each configured mirror
// (see defaultMirrorPrefixes). checksums.txt is taken from the first source
// that serves it, and the archive is accepted from the first source whose
// bytes match that digest — a mirror serving a corrupt or stale copy simply
// fails over to the next source.
func (u *Updater) Apply(ctx context.Context, tag string, out io.Writer) error {
	versionNoV := strings.TrimPrefix(strings.TrimSpace(tag), "v")
	archive := u.archiveName(versionNoV)
	base := fmt.Sprintf("%s/%s", strings.TrimRight(u.ReleaseBaseURL, "/"), tag)
	archiveURL := base + "/" + archive

	fmt.Fprintf(out, "downloading %s ...\n", archive)

	sums, _, err := u.fetch(ctx, base+"/"+checksumsFile, checksumsFile, out, nil)
	if err != nil {
		return fmt.Errorf("selfupdate: download checksums: %w", err)
	}
	want, err := checksumFor(sums, archive)
	if err != nil {
		return err
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
// error. An attempt that stops delivering data for the stall timeout is
// aborted (cancelling the request context also cuts the in-flight body read)
// so the caller can move on to the next source.
func (u *Updater) download(ctx context.Context, url string) ([]byte, error) {
	stall := u.StallTimeout
	if stall <= 0 {
		stall = defaultStallTimeout
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

	// Watchdog: cancel the request when no bytes have arrived for `stall`. A
	// separate goroutine plus an atomic timestamp keeps the check out of the
	// read loop and avoids timer-reset races.
	var last atomic.Int64
	var stalled atomic.Bool
	last.Store(time.Now().UnixNano())
	done := make(chan struct{})
	go func() {
		interval := stall / 4
		if interval < 25*time.Millisecond {
			interval = 25 * time.Millisecond
		}
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if time.Since(time.Unix(0, last.Load())) >= stall {
					stalled.Store(true)
					cancel()
					return
				}
			}
		}
	}()
	defer close(done)

	var buf bytes.Buffer
	chunk := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(chunk)
		if n > 0 {
			last.Store(time.Now().UnixNano())
			buf.Write(chunk[:n])
		}
		if err == io.EOF {
			return buf.Bytes(), nil
		}
		if err != nil {
			if stalled.Load() {
				return nil, fmt.Errorf("no data for %s (stalled)", stall)
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
