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
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	docxversion "github.com/getan/golder/extension/plugins/docx/version"
	"github.com/getan/golder/internal/githubmirror"
)

var docxReleaseVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

// DocxHelperVersion is the helper version this build expects.
func DocxHelperVersion() string { return docxversion.Version }

// DocxRelease identifies the exact first-party DOCX helper package a user is
// asked to download. Discovery never downloads or executes it.
type DocxRelease struct {
	Version     string
	Archive     string
	ArchiveURL  string
	ChecksumURL string
	Binary      string
}

// DocxDownloadProgress is one downloader progress sample. Stage is one of
// checking, connecting, downloading, checksums, verifying, extracting,
// installing or complete. Total is zero when the server omits Content-Length.
type DocxDownloadProgress struct {
	Stage         string
	Source        string
	Downloaded    int64
	Total         int64
	Attempt       int
	TotalAttempts int
}

func docxRelease(hostVersion, goos, goarch, base string) (DocxRelease, error) {
	hostVersion = strings.TrimPrefix(strings.TrimSpace(hostVersion), "v")
	if !docxReleaseVersion.MatchString(hostVersion) {
		return DocxRelease{}, errors.New("a stable Golder release is required to download the DOCX helper")
	}
	switch goos {
	case "darwin", "linux", "windows":
	default:
		return DocxRelease{}, errors.New("the matching DOCX helper is unavailable on this platform")
	}
	if goarch != "amd64" && goarch != "arm64" {
		return DocxRelease{}, errors.New("the matching DOCX helper is unavailable on this architecture")
	}
	binary := "golder-docx"
	if goos == "windows" {
		binary += ".exe"
	}
	// The asset name carries the host release and the independently versioned
	// helper; .goreleaser.yaml uses the same template.
	archive := fmt.Sprintf("golder-docx_%s_helper%s_%s_%s.tar.gz", hostVersion, docxversion.Version, goos, goarch)
	releaseURL := strings.TrimRight(base, "/") + "/v" + hostVersion
	return DocxRelease{
		Version: docxversion.Version, Archive: archive,
		ArchiveURL:  releaseURL + "/" + archive,
		ChecksumURL: releaseURL + "/checksums.txt", Binary: binary,
	}, nil
}

// DocxReleaseFor reports the official URLs for a Golder release and the
// independently versioned DOCX helper contained in it.
func DocxReleaseFor(version string) (DocxRelease, error) {
	return docxRelease(version, runtime.GOOS, runtime.GOARCH, "https://github.com/getan/golder/releases/download")
}

// InstallDocxRelease downloads and verifies the helper after an explicit user
// action. It does not create, approve, enable or execute an MCP source.
func InstallDocxRelease(ctx context.Context, version string) (ManagedPackage, error) {
	return InstallDocxReleaseWithProgress(ctx, version, nil)
}

// InstallDocxReleaseWithProgress is InstallDocxRelease with byte and stage
// progress. Callbacks run on the download goroutine and must not block.
func InstallDocxReleaseWithProgress(ctx context.Context, version string, progress func(DocxDownloadProgress)) (ManagedPackage, error) {
	release, err := DocxReleaseFor(version)
	if err != nil {
		return ManagedPackage{}, err
	}
	client := &http.Client{Timeout: 5 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 || req.URL.Scheme != "https" || req.URL.User != nil {
			return errors.New("untrusted release redirect")
		}
		return nil
	}}
	return installDocxReleaseWithProgress(ctx, release, client, progress)
}

func docxDownloadProgressAdapter(progress func(DocxDownloadProgress), attempt, total int) func(LarkDownloadProgress) {
	if progress == nil {
		return nil
	}
	return func(p LarkDownloadProgress) {
		progress(DocxDownloadProgress{
			Stage: p.Stage, Source: p.Source, Downloaded: p.Downloaded, Total: p.Total,
			Attempt: attempt, TotalAttempts: total,
		})
	}
}

func docxDownloadAttempt(ctx context.Context, client *http.Client, rawURL string, max int64, timeout time.Duration, stage string, progress func(DocxDownloadProgress), attempt, total int) ([]byte, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	return releaseDownloadProgress(ctx, client, rawURL, max, stage, docxDownloadProgressAdapter(progress, attempt, total))
}

func docxChecksumFor(checksums []byte, archive string) (string, error) {
	want := ""
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == archive {
			if want != "" {
				return "", errors.New("duplicate DOCX release checksum")
			}
			want = strings.ToLower(fields[0])
		}
	}
	if len(want) != sha256.Size*2 {
		return "", errors.New("matching DOCX release checksum was not found")
	}
	if _, err := hex.DecodeString(want); err != nil {
		return "", errors.New("invalid DOCX release checksum")
	}
	return want, nil
}

// fetchDocxChecksum tries direct GitHub first. If it is unreachable, mirror
// copies are cross-checked: they must agree on the archive digest, so one
// mirror cannot vouch for bytes it poisoned.
func fetchDocxChecksum(ctx context.Context, client *http.Client, release DocxRelease, progress func(DocxDownloadProgress)) (string, error) {
	sources := githubmirror.Apply(release.ChecksumURL, githubmirror.Prefixes())
	trusted := ""
	var directErr error
	for i, src := range sources {
		body, err := docxDownloadAttempt(ctx, client, src, 2<<20, 45*time.Second, "checksums", progress, i+1, len(sources))
		if err != nil {
			if i == 0 {
				directErr = err
			}
			continue
		}
		sum, err := docxChecksumFor(body, release.Archive)
		if err != nil {
			if i == 0 {
				directErr = err
			}
			continue
		}
		if trusted == "" {
			trusted = sum
			continue
		}
		if trusted != sum {
			return "", fmt.Errorf("DOCX release checksum mirrors disagree for %s; possible tampering", release.Archive)
		}
	}
	if trusted != "" {
		return trusted, nil
	}
	if directErr != nil {
		return "", fmt.Errorf("DOCX release checksum unavailable from GitHub or mirrors: %w", directErr)
	}
	return "", errors.New("DOCX release checksum unavailable from GitHub or mirrors")
}

// fetchDocxArchive tries direct GitHub, then each configured mirror. The
// checksum was already trusted by fetchDocxChecksum, so a mirror serving
// different bytes is rejected and the next source is attempted.
func fetchDocxArchive(ctx context.Context, client *http.Client, release DocxRelease, want string, progress func(DocxDownloadProgress)) ([]byte, error) {
	sources := githubmirror.Apply(release.ArchiveURL, githubmirror.Prefixes())
	var last error
	for i, src := range sources {
		archive, err := docxDownloadAttempt(ctx, client, src, 128<<20, 90*time.Second, "downloading", progress, i+1, len(sources))
		if err != nil {
			last = err
			continue
		}
		digest := sha256.Sum256(archive)
		if hex.EncodeToString(digest[:]) != want {
			last = errors.New("DOCX release archive checksum mismatch")
			continue
		}
		return archive, nil
	}
	if last == nil {
		last = errors.New("DOCX release download failed")
	}
	return nil, fmt.Errorf("DOCX release download failed from GitHub and mirrors: %w", last)
}

func installDocxReleaseWithProgress(ctx context.Context, release DocxRelease, client *http.Client, progress func(DocxDownloadProgress)) (ManagedPackage, error) {
	report := func(stage string) {
		if progress != nil {
			progress(DocxDownloadProgress{Stage: stage})
		}
	}
	report("checking")
	want, err := fetchDocxChecksum(ctx, client, release, progress)
	if err != nil {
		return ManagedPackage{}, err
	}
	archive, err := fetchDocxArchive(ctx, client, release, want, progress)
	if err != nil {
		return ManagedPackage{}, err
	}
	report("verifying")
	digest := sha256.Sum256(archive)
	if hex.EncodeToString(digest[:]) != want {
		return ManagedPackage{}, errors.New("DOCX release archive checksum mismatch")
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return ManagedPackage{}, errors.New("invalid DOCX release archive")
	}
	defer gz.Close()
	expanded := &io.LimitedReader{R: gz, N: (128 << 20) + 1}
	reader := tar.NewReader(expanded)
	var binary []byte
	for {
		hdr, err := reader.Next()
		if expanded.N <= 0 {
			return ManagedPackage{}, errors.New("DOCX release archive expands beyond the size limit")
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return ManagedPackage{}, errors.New("invalid DOCX release archive")
		}
		if strings.TrimPrefix(hdr.Name, "./") != release.Binary {
			continue
		}
		if binary != nil || hdr.Typeflag != tar.TypeReg || hdr.Size <= 0 || hdr.Size > 96<<20 {
			return ManagedPackage{}, errors.New("invalid DOCX helper in release archive")
		}
		binary, err = io.ReadAll(io.LimitReader(reader, (96<<20)+1))
		if err != nil || len(binary) == 0 || len(binary) > 96<<20 {
			return ManagedPackage{}, errors.New("cannot read DOCX helper from release archive")
		}
	}
	if len(binary) == 0 {
		return ManagedPackage{}, errors.New("DOCX helper was not found in the release archive")
	}
	if err := ctx.Err(); err != nil {
		return ManagedPackage{}, err
	}
	report("extracting")
	tmp, err := os.MkdirTemp("", "golder-docx-install-")
	if err != nil {
		return ManagedPackage{}, errors.New("cannot create temporary DOCX installer")
	}
	defer os.RemoveAll(tmp)
	source := filepath.Join(tmp, release.Binary)
	if err := os.WriteFile(source, binary, 0o700); err != nil {
		return ManagedPackage{}, errors.New("cannot stage the DOCX helper")
	}
	binaryDigest := sha256.Sum256(binary)
	report("installing")
	pkg, err := InstallManagedPackage(PackageInstallOptions{
		ID: "docx", Version: release.Version, Source: source,
		SHA256: hex.EncodeToString(binaryDigest[:]),
	})
	if err != nil {
		return ManagedPackage{}, err
	}
	report("complete")
	return pkg, nil
}
