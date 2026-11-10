package mygoui

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/getan/golder/internal/downloadnet"
	"github.com/getan/golder/internal/selfupdate"
)

const maxGUIUpdateBytes int64 = 512 << 20

// guiDownload is built only from the exact platform asset in GitHub's release
// metadata. The digest protects against incomplete or corrupt downloads.
type guiDownload struct {
	Tag, Name, URL, SHA256 string
	Size                   int64
	GOOS, GOARCH           string
}

func (d guiDownload) validate() error {
	name := guiAssetName(d.Tag, d.GOOS, d.GOARCH)
	if name == "" || name != d.Name || filepath.Base(d.Name) != d.Name {
		return errors.New("invalid GUI update filename")
	}
	expectedURL := "https://github.com/" + selfupdate.Repo + "/releases/download/" + url.PathEscape(d.Tag) + "/" + url.PathEscape(d.Name)
	if d.URL != expectedURL {
		return errors.New("invalid GUI update URL")
	}
	if d.Size <= 0 || d.Size > maxGUIUpdateBytes {
		return errors.New("invalid GUI update size")
	}
	digest, err := hex.DecodeString(d.SHA256)
	if err != nil || len(digest) != sha256.Size {
		return errors.New("missing or invalid GUI update SHA-256")
	}
	return nil
}

// GitHub redirects release downloads to its asset CDN. Never follow a
// downgrade or a redirect to an unrelated host, even if the digest matches.
func guiDownloadRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 || req.URL.Scheme != "https" || req.URL.User != nil || req.URL.Port() != "" {
		return errors.New("unsafe GUI update redirect")
	}
	switch strings.ToLower(req.URL.Hostname()) {
	case "github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com", "github-production-release-asset-2e65be.s3.amazonaws.com":
		return nil
	default:
		return errors.New("GUI update redirected to an unexpected host")
	}
}

func newGUIDownloadClient() *http.Client {
	return &http.Client{Timeout: 15 * time.Minute, Transport: downloadnet.Transport(), CheckRedirect: guiDownloadRedirect}
}

type guiProgressWriter struct {
	done, total int64
	last        time.Time
	report      func(int64, int64)
}

func (p *guiProgressWriter) Write(b []byte) (int, error) {
	p.done += int64(len(b))
	if p.report != nil && (p.done >= p.total || time.Since(p.last) >= 250*time.Millisecond) {
		p.last = time.Now()
		p.report(p.done, p.total)
	}
	return len(b), nil
}

// downloadGUIPackage writes a private temporary file in Downloads, verifies
// the exact size and GitHub-provided digest, marks it as internet-downloaded,
// then publishes it without replacing an existing user file. Failures and
// cancellations remove the partial file and never open or install anything.
func downloadGUIPackage(ctx context.Context, client *http.Client, pkg guiDownload, dir string, mark func(string, string) error, progress func(int64, int64)) (string, error) {
	if err := pkg.validate(); err != nil {
		return "", err
	}
	if client == nil || mark == nil || !filepath.IsAbs(dir) {
		return "", errors.New("download directory or client is unavailable")
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return "", errors.New("the Downloads folder is unavailable")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pkg.URL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := client.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return "", fmt.Errorf("download request failed: %w", urlErr.Err)
		}
		return "", fmt.Errorf("download request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download server returned %s", resp.Status)
	}
	if resp.ContentLength >= 0 && resp.ContentLength != pkg.Size {
		return "", errors.New("download size does not match GitHub's release metadata")
	}
	tmp, err := os.CreateTemp(dir, ".golder-gui-*.part")
	if err != nil {
		return "", fmt.Errorf("create download: %w", err)
	}
	tmpName := tmp.Name()
	closed := false
	defer func() {
		if !closed {
			_ = tmp.Close()
		}
		_ = os.Remove(tmpName)
	}()
	hash := sha256.New()
	counter := &guiProgressWriter{total: pkg.Size, report: progress}
	n, err := io.CopyBuffer(io.MultiWriter(tmp, hash, counter), io.LimitReader(resp.Body, pkg.Size+1), make([]byte, 64<<10))
	if err != nil {
		return "", fmt.Errorf("download failed: %w", err)
	}
	if n != pkg.Size {
		return "", errors.New("download size does not match GitHub's release metadata")
	}
	expected, _ := hex.DecodeString(pkg.SHA256)
	if subtle.ConstantTimeCompare(hash.Sum(nil), expected) != 1 {
		return "", errors.New("download SHA-256 does not match GitHub's release metadata")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		return "", fmt.Errorf("save download: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("save download: %w", err)
	}
	closed = true
	if err := mark(tmpName, pkg.URL); err != nil {
		return "", fmt.Errorf("mark internet download: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	for i := 0; i < 100; i++ {
		name := guiDownloadName(pkg.Name, i)
		destination := filepath.Join(dir, name)
		if err := os.Link(tmpName, destination); err == nil {
			return destination, nil
		} else if !errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("save verified download without overwriting a file: %w", err)
		}
	}
	return "", errors.New("too many downloads with the same name")
}

func guiDownloadName(name string, index int) string {
	if index == 0 {
		return name
	}
	ext := filepath.Ext(name)
	if strings.HasSuffix(name, ".tar.gz") {
		ext = ".tar.gz"
	}
	return fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(name, ext), index, ext)
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(b)
}

// Recheck a saved file after the user clicks Open: it may have been removed or
// changed since download. Never follow a replacement symlink.
func verifyDownloadedGUIFile(ctx context.Context, pkg guiDownload, path string, mark func(string, string) error) error {
	if err := pkg.validate(); err != nil {
		return err
	}
	if mark == nil || !filepath.IsAbs(path) {
		return errors.New("downloaded file is unavailable")
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Size() != pkg.Size {
		return errors.New("downloaded file is missing or changed; download it again")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return errors.New("downloaded file was replaced; download it again")
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(contextReader{ctx: ctx, r: f}, pkg.Size+1))
	if err != nil {
		return err
	}
	expected, _ := hex.DecodeString(pkg.SHA256)
	if n != pkg.Size || subtle.ConstantTimeCompare(hash.Sum(nil), expected) != 1 {
		return errors.New("downloaded file was modified; download it again")
	}
	after, err := os.Lstat(path)
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(opened, after) || after.Size() != pkg.Size {
		return errors.New("downloaded file was replaced; download it again")
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := mark(path, pkg.URL); err != nil {
		return fmt.Errorf("mark internet download: %w", err)
	}
	return nil
}
