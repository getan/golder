package mygoui

import (
	"bytes"
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
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

type guiRoundTrip func(*http.Request) (*http.Response, error)

func (f guiRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func guiTestPackage(t *testing.T, content []byte, goos, arch string) guiDownload {
	t.Helper()
	name := guiAssetName("v1.2.3", goos, arch)
	sum := sha256.Sum256(content)
	release := guiRelease{Tag: "v1.2.3", Assets: []guiReleaseAsset{{
		Name: name, State: "uploaded", Size: int64(len(content)), Digest: "sha256:" + hex.EncodeToString(sum[:]),
	}}}
	pkg, available, err := guiReleaseDownload(release, "1.2.2", goos, arch)
	if err != nil || !available {
		t.Fatalf("select release: available=%v err=%v", available, err)
	}
	return pkg
}

func guiTestClient(content []byte, status int, length int64) *http.Client {
	return &http.Client{Transport: guiRoundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)),
			Body: io.NopCloser(bytes.NewReader(content)), ContentLength: length, Header: make(http.Header), Request: r,
		}, nil
	})}
}

func TestGUIReleaseDownloadRequiresValidMetadata(t *testing.T) {
	content := []byte("package contents")
	for _, platform := range []struct{ goos, arch string }{
		{"darwin", "arm64"}, {"darwin", "amd64"}, {"windows", "amd64"}, {"linux", "amd64"}, {"linux", "arm64"},
	} {
		t.Run(platform.goos+"-"+platform.arch, func(t *testing.T) {
			pkg := guiTestPackage(t, content, platform.goos, platform.arch)
			if err := pkg.validate(); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(content)
			release := guiRelease{Tag: pkg.Tag, Assets: []guiReleaseAsset{{Name: pkg.Name, State: "uploaded", Size: pkg.Size, Digest: "sha256:" + hex.EncodeToString(sum[:])}}}
			if _, available, err := guiReleaseDownload(release, "1.2.3", platform.goos, platform.arch); err != nil || available {
				t.Fatalf("same version: available=%v err=%v", available, err)
			}
			for _, mutate := range []func(*guiReleaseAsset){
				func(a *guiReleaseAsset) { a.Digest = "" },
				func(a *guiReleaseAsset) { a.Digest = "sha256:abcd" },
				func(a *guiReleaseAsset) { a.Digest = "sha512:" + strings.Repeat("a", 64) },
				func(a *guiReleaseAsset) { a.State = "new" },
				func(a *guiReleaseAsset) { a.Size = 0 },
				func(a *guiReleaseAsset) { a.Size = maxGUIUpdateBytes + 1 },
			} {
				bad := release
				bad.Assets = append([]guiReleaseAsset(nil), release.Assets...)
				mutate(&bad.Assets[0])
				if _, available, err := guiReleaseDownload(bad, "1.2.2", platform.goos, platform.arch); err == nil || available {
					t.Fatalf("bad metadata accepted: %+v", bad.Assets[0])
				}
			}
		})
	}
	for _, tag := range []string{"v1.2", "v1.2.3.4", "v1..3", "v1.2.3-rc1", "v1.2.3/../../x", "1.2.3"} {
		if name := guiAssetName(tag, "darwin", "arm64"); name != "" {
			t.Errorf("unsafe tag %q produced %q", tag, name)
		}
	}
}

func TestDownloadGUIPackageAndPreserveExistingFile(t *testing.T) {
	content := []byte("verified GUI package")
	for _, platform := range []struct{ goos, arch string }{{"darwin", "arm64"}, {"windows", "amd64"}, {"linux", "arm64"}} {
		t.Run(platform.goos, func(t *testing.T) {
			pkg := guiTestPackage(t, content, platform.goos, platform.arch)
			dir := t.TempDir()
			original := filepath.Join(dir, pkg.Name)
			if err := os.WriteFile(original, []byte("existing user file"), 0o600); err != nil {
				t.Fatal(err)
			}
			var marked, progress bool
			mark := func(path, source string) error {
				marked = true
				if !strings.HasSuffix(path, ".part") || source != pkg.URL {
					t.Errorf("bad mark arguments: %q %q", path, source)
				}
				return nil
			}
			path, err := downloadGUIPackage(context.Background(), guiTestClient(content, 200, int64(len(content))), pkg, dir, mark, func(done, total int64) {
				progress = done == int64(len(content)) && total == int64(len(content))
			})
			if err != nil {
				t.Fatal(err)
			}
			if path != filepath.Join(dir, guiDownloadName(pkg.Name, 1)) || !marked || !progress {
				t.Fatalf("path=%q marked=%v progress=%v", path, marked, progress)
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, content) {
				t.Fatalf("download contents=%q err=%v", got, err)
			}
			old, err := os.ReadFile(original)
			if err != nil || string(old) != "existing user file" {
				t.Fatalf("existing file changed: %q, %v", old, err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 2 {
				t.Fatalf("temporary file left behind: %v, %v", entries, err)
			}
		})
	}
}

func TestDownloadGUIPackageFailuresCleanUp(t *testing.T) {
	content := []byte("correct bytes")
	for _, tc := range []struct {
		name    string
		body    []byte
		status  int
		length  int64
		markErr bool
		cancel  bool
	}{
		{"http error", content, 404, int64(len(content)), false, false},
		{"wrong content length", content, 200, 1, false, false},
		{"truncated", content[:5], 200, -1, false, false},
		{"too long", append(bytes.Clone(content), '!'), 200, -1, false, false},
		{"digest mismatch", []byte("wrong content"), 200, int64(len(content)), false, false},
		{"mark fails", content, 200, int64(len(content)), true, false},
		{"cancelled", content, 200, int64(len(content)), false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pkg := guiTestPackage(t, content, "darwin", "arm64")
			dir := t.TempDir()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			path, err := downloadGUIPackage(ctx, guiTestClient(tc.body, tc.status, tc.length), pkg, dir, func(_, _ string) error {
				if tc.markErr {
					return errors.New("mark failed")
				}
				return nil
			}, func(_, _ int64) {
				if tc.cancel {
					cancel()
				}
			})
			if err == nil || path != "" {
				t.Fatalf("bad download accepted: path=%q err=%v", path, err)
			}
			entries, readErr := os.ReadDir(dir)
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("partial download remains: %v, %v", entries, readErr)
			}
		})
	}
}

func TestGUIDownloadRejectsUnsafeURLAndRedirects(t *testing.T) {
	content := []byte("package")
	pkg := guiTestPackage(t, content, "darwin", "arm64")
	for _, raw := range []string{
		"http://github.com/getan/golder/releases/download/v1.2.3/" + pkg.Name,
		"https://evil.example/getan/golder/releases/download/v1.2.3/" + pkg.Name,
		pkg.URL + "?other=1",
	} {
		bad := pkg
		bad.URL = raw
		if err := bad.validate(); err == nil {
			t.Errorf("accepted URL %q", raw)
		}
	}
	for _, tc := range []struct {
		raw  string
		safe bool
	}{
		{"https://github.com/path", true},
		{"https://release-assets.githubusercontent.com/path?signature=secret", true},
		{"https://objects.githubusercontent.com/path", true},
		{"http://release-assets.githubusercontent.com/path", false},
		{"https://release-assets.githubusercontent.com.evil.example/path", false},
		{"https://user@github.com/path", false},
		{"https://github.com:444/path", false},
	} {
		u, err := url.Parse(tc.raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := guiDownloadRedirect(&http.Request{URL: u}, nil) == nil; got != tc.safe {
			t.Errorf("redirect %q: safe=%v, want %v", tc.raw, got, tc.safe)
		}
	}
	u, _ := url.Parse("https://github.com/path")
	if err := guiDownloadRedirect(&http.Request{URL: u}, make([]*http.Request, 10)); err == nil {
		t.Fatal("redirect limit was not enforced")
	}
}

func TestVerifyDownloadedGUIFile(t *testing.T) {
	content := []byte("downloaded package")
	pkg := guiTestPackage(t, content, "darwin", "arm64")
	dir := t.TempDir()
	path := filepath.Join(dir, pkg.Name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	marked := false
	mark := func(got, source string) error {
		marked = got == path && source == pkg.URL
		return nil
	}
	if err := verifyDownloadedGUIFile(context.Background(), pkg, path, mark); err != nil || !marked {
		t.Fatalf("verify valid file: marked=%v err=%v", marked, err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), len(content)), 0o600); err != nil {
		t.Fatal(err)
	}
	marked = false
	if err := verifyDownloadedGUIFile(context.Background(), pkg, path, mark); err == nil || marked {
		t.Fatalf("modified file accepted: marked=%v err=%v", marked, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "elsewhere"), path); err == nil {
		if err := verifyDownloadedGUIFile(context.Background(), pkg, path, mark); err == nil {
			t.Fatal("symlink accepted")
		}
	}
}

func TestGUIUpdateCancelAndPrompt(t *testing.T) {
	a := testApp()
	ctx, cancel, seq := a.beginGUIUpdateOperation("checking", time.Minute)
	defer cancel()
	a.activateGUIUpdate()
	if !a.updateAfterCheck {
		t.Fatal("click during background check should request download afterwards")
	}
	a.cancelGUIUpdate()
	if ctx.Err() == nil || a.updateBusy || a.updateAfterCheck || a.finishGUIUpdateOperation(seq) {
		t.Fatal("cancellation did not invalidate the pending operation")
	}
	a.updateTag, a.updatePath, a.updateDialogOpen = "v1.2.3", "/downloads/golder.dmg", true
	tt := ui.NewTester(a.view, 1200, 800)
	open, _ := guiUpdateInstructions(runtime.GOOS)
	for _, want := range []string{"GUI v1.2.3 downloaded", "Later", "Show in folder", open} {
		if !tt.HasText(want) {
			t.Fatalf("download prompt missing %q: %q", want, tt.Texts())
		}
	}
	if err := tt.Click("Later"); err != nil || a.updateDialogOpen {
		t.Fatalf("Later did not dismiss prompt: open=%v err=%v", a.updateDialogOpen, err)
	}
	a.activateGUIUpdate()
	if !a.updateDialogOpen {
		t.Fatal("clicking Update did not reopen the downloaded package prompt")
	}
}

func TestGUIUpdateInstructionsCoverEachPlatform(t *testing.T) {
	for _, tc := range []struct {
		goos, label, instruction string
	}{
		{"darwin", "Open disk image", "Applications"},
		{"windows", "Open installer", "installer"},
		{"linux", "Open archive", "package manager"},
	} {
		label, message := guiUpdateInstructions(tc.goos)
		if label != tc.label || !strings.Contains(message, tc.instruction) {
			t.Errorf("%s: label=%q message=%q", tc.goos, label, message)
		}
	}
}

// This opt-in test downloads a real GitHub release to a temporary directory;
// it never opens the package or installs it.
func TestLiveGUIDownload(t *testing.T) {
	if os.Getenv("GOLDER_LIVE_GUI_DOWNLOAD_TEST") != "1" {
		t.Skip("set GOLDER_LIVE_GUI_DOWNLOAD_TEST=1 to download a real release")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	release, err := fetchGUIRelease(ctx, &http.Client{Timeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []struct{ goos, arch string }{{"darwin", "arm64"}, {"darwin", "amd64"}, {"windows", "amd64"}, {"linux", "amd64"}, {"linux", "arm64"}} {
		if _, ok, err := guiReleaseDownload(release, "0.0.1", p.goos, p.arch); err != nil || !ok {
			t.Fatalf("missing live %s/%s package: available=%v err=%v", p.goos, p.arch, ok, err)
		}
	}
	pkg, _, _ := guiReleaseDownload(release, "0.0.1", "darwin", "arm64")
	path, err := downloadGUIPackage(ctx, newGUIDownloadClient(), pkg, t.TempDir(), markGUIInternetDownload, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyDownloadedGUIFile(ctx, pkg, path, markGUIInternetDownload); err != nil {
		t.Fatal(err)
	}
}
