package extensions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type officialCLITransport func(*http.Request) (*http.Response, error)

func (f officialCLITransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func progressCLIInstaller(t *testing.T, kind OfficialCLIKind) (*OfficialCLIInstaller, []byte) {
	t.Helper()
	path, name := "lark-cli", "lark-cli"
	if kind == OfficialProjectCLI {
		path, name = "package/bin/meegle-darwin-arm64", "meegle"
	}
	binary := append([]byte{0xfe, 0xed, 0xfa, 0xcf}, bytes.Repeat([]byte("native binary"), 100)...)
	archive := officialCLITestArchive(t, path, binary)
	spec := OfficialCLIInstallSpec{Version: "1.2.3", ArchiveURL: "https://github.com/test/cli.tgz", ArchivePath: path, BinaryName: name}
	if kind == OfficialProjectCLI {
		sum := sha512.Sum512(archive)
		spec.SHA512 = base64.StdEncoding.EncodeToString(sum[:])
	} else {
		sum := sha256.Sum256(archive)
		spec.SHA256 = hex.EncodeToString(sum[:])
	}
	return &OfficialCLIInstaller{
		Root: t.TempDir(), GOOS: "darwin", GOARCH: "arm64", SkipCommonDiscovery: true,
		Specs: map[OfficialCLIKind]OfficialCLIInstallSpec{kind: spec},
	}, archive
}

func TestOfficialCLIProgressStreamsBeforeSlowDownloadCompletes(t *testing.T) {
	for _, kind := range []OfficialCLIKind{OfficialLarkCLI, OfficialProjectCLI} {
		for _, known := range []bool{true, false} {
			name := string(kind) + map[bool]string{true: "/known-size", false: "/chunked"}[known]
			t.Run(name, func(t *testing.T) {
				i, archive := progressCLIInstaller(t, kind)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				reader, writer := io.Pipe()
				defer reader.Close()
				defer writer.Close()
				release := make(chan struct{})
				go func() {
					_, _ = writer.Write(archive[:len(archive)/2])
					select {
					case <-release:
						_, _ = writer.Write(archive[len(archive)/2:])
						_ = writer.Close()
					case <-ctx.Done():
						_ = writer.CloseWithError(ctx.Err())
					}
				}()
				total := int64(-1)
				if known {
					total = int64(len(archive))
				}
				i.Client = &http.Client{Transport: officialCLITransport(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, ContentLength: total, Body: reader}, nil
				})}
				events := make(chan OfficialCLIInstallProgress, 128)
				i.Progress = func(p OfficialCLIInstallProgress) { events <- p }
				finished := make(chan error, 1)
				go func() {
					_, err := i.Install(ctx, kind)
					finished <- err
				}()
				var seen []OfficialCLIInstallProgress
				for {
					select {
					case p := <-events:
						seen = append(seen, p)
						if p.Phase == "downloading" && p.Downloaded > 0 {
							if p.Downloaded >= int64(len(archive)) || p.Total != max(total, 0) {
								t.Fatalf("incorrect streaming progress: %+v", p)
							}
							goto received
						}
					case <-ctx.Done():
						t.Fatal("no progress while the download was stalled")
					}
				}
			received:
				select {
				case err := <-finished:
					t.Fatalf("download completed before the server released the rest: %v", err)
				default:
				}
				close(release)
				select {
				case err := <-finished:
					if err != nil {
						t.Fatal(err)
					}
				case <-ctx.Done():
					t.Fatal("install did not finish after the remaining bytes arrived")
				}
				close(events)
				for p := range events {
					seen = append(seen, p)
				}
				var phases []string
				var last int64
				for _, p := range seen {
					if p.Kind != kind || p.Downloaded < last {
						t.Fatalf("wrong kind or regressing progress: %+v", seen)
					}
					last = p.Downloaded
					if len(phases) == 0 || phases[len(phases)-1] != p.Phase {
						phases = append(phases, p.Phase)
					}
				}
				want := []string{"checking", "connecting", "downloading", "verifying", "extracting", "installing", "complete"}
				if !reflect.DeepEqual(phases, want) || last != int64(len(archive)) {
					t.Fatalf("phases=%v bytes=%d, want %v bytes=%d", phases, last, want, len(archive))
				}
				// A cached install needs no network and must not pretend to download.
				var cached []string
				i.Progress = func(p OfficialCLIInstallProgress) { cached = append(cached, p.Phase) }
				i.Client = &http.Client{Transport: officialCLITransport(func(*http.Request) (*http.Response, error) {
					t.Error("cached installation performed another download")
					return nil, errors.New("unexpected request")
				})}
				if _, err := i.Install(ctx, kind); err != nil || !reflect.DeepEqual(cached, []string{"checking", "complete"}) {
					t.Fatalf("cached install: %v, phases=%v", err, cached)
				}
			})
		}
	}
}

func TestOfficialCLIProgressFailuresDoNotPublishAnInstall(t *testing.T) {
	for _, kind := range []OfficialCLIKind{OfficialLarkCLI, OfficialProjectCLI} {
		for _, failure := range []string{"HTTP", "checksum", "truncated", "cancelled", "cancelled-before-publish", "oversized"} {
			t.Run(string(kind)+"/"+failure, func(t *testing.T) {
				i, archive := progressCLIInstaller(t, kind)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				spec := i.Specs[kind]
				if failure == "checksum" {
					spec.SHA256, spec.SHA512 = "00", ""
					i.Specs[kind] = spec
				}
				status, total := 200, int64(len(archive))
				if failure == "HTTP" {
					status = 503
				} else if failure == "truncated" {
					total++
				} else if failure == "oversized" {
					total = maxOfficialCLIArchive + 1
				}
				i.Client = &http.Client{Transport: officialCLITransport(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: status, ContentLength: total, Body: io.NopCloser(bytes.NewReader(archive))}, nil
				})}
				var phases []string
				i.Progress = func(p OfficialCLIInstallProgress) {
					phases = append(phases, p.Phase)
					if failure == "cancelled" && p.Downloaded > 0 || failure == "cancelled-before-publish" && p.Phase == "installing" {
						cancel()
					}
				}
				if _, err := i.Install(ctx, kind); err == nil {
					t.Fatal("failed download was reported as installed")
				}
				if strings.Contains(strings.Join(phases, ","), "complete") {
					t.Fatalf("failure emitted a successful completion: %v", phases)
				}
				if _, err := os.Stat(filepath.Join(i.Root, string(kind), spec.Version)); !os.IsNotExist(err) {
					t.Fatalf("failed install was published: %v", err)
				}
				if partial, _ := filepath.Glob(filepath.Join(i.Root, string(kind), ".install-*")); len(partial) != 0 {
					t.Fatalf("partial installations left behind: %v", partial)
				}
			})
		}
	}
}

// Explicit network smoke test: only downloads and verifies the official native
// archives in t.TempDir. Never executes the CLI, logs in or changes user state.
func TestOfficialCLIReleaseDownload(t *testing.T) {
	if os.Getenv("GOLDER_TEST_OFFICIAL_CLI_DOWNLOAD") != "1" {
		t.Skip("set GOLDER_TEST_OFFICIAL_CLI_DOWNLOAD=1 to verify both official release downloads")
	}
	for _, kind := range []OfficialCLIKind{OfficialLarkCLI, OfficialProjectCLI} {
		t.Run(string(kind), func(t *testing.T) {
			i := NewOfficialCLIInstaller()
			i.Root, i.SkipCommonDiscovery = t.TempDir(), true
			var lastPhase string
			i.Progress = func(p OfficialCLIInstallProgress) {
				if p.Phase != lastPhase {
					t.Logf("%s: %s, received=%d total=%d", kind, p.Phase, p.Downloaded, p.Total)
					lastPhase = p.Phase
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			defer cancel()
			installed, err := i.Install(ctx, kind)
			if err != nil {
				t.Fatal(err)
			}
			found, ok := i.Discover(kind, "")
			resolved, _ := filepath.EvalSymlinks(installed.Executable)
			if !ok || found.Executable != resolved || lastPhase != "complete" {
				t.Fatalf("installed=%+v discovered=%+v complete=%s", installed, found, lastPhase)
			}
		})
	}
}
