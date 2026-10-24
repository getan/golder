package agenttool

// Starting a browser used to be three things a person had to remember: a
// profile directory, a control socket under the workspace, and the export that
// tells golder where the socket is. None of that is a decision — it is setup.
// The agent runs it itself: inside a tuios pane it opens a pane with the right
// argv, waits for the socket, adopts it for the session and records the pane's
// identity so a later close can prove what it is closing.
//
// Two programs can be that pane. golder's own binary is the default once main
// has told this package that `golder browse` exists (ConfigureEmbeddedBrowser);
// an installed kitty-browse is the compatible fallback. This package never
// guesses that some other executable understands "browse": the capability is
// set by main, from the process that actually implements it.

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/getan/golder/internal/browserctl"
	"github.com/getan/golder/internal/tuiosctl"
)

const (
	browserProfileMaxAge = 14 * 24 * time.Hour
	// browserSocketWait bounds how long a new pane has to create its endpoint.
	browserSocketWait = 25 * time.Second
	// browserProbeTimeout bounds the identity probe that follows.
	browserProbeTimeout = 5 * time.Second
)

var (
	embeddedBrowserMu sync.RWMutex
	// embeddedBrowserExecutable is this process's own binary, set only by main
	// when it also wires `golder <exe> browse` to a real implementation.
	embeddedBrowserExecutable string
	// embeddedBrowserErr keeps a rejected configuration visible: a caller that
	// asked for the built-in pane gets a reason, not a silent fallback.
	embeddedBrowserErr error
)

// ConfigureEmbeddedBrowser tells the browser tool that this executable can
// start a browser pane itself, with `"<exe>" browse -profile-dir DIR
// -control-socket SOCKET URL`. main calls it once with its own
// os.Executable(); nothing here assumes an arbitrary binary supports "browse".
//
// An empty executable disables the built-in pane again (kitty-browse remains
// the fallback), which is also how tests restore the state they found. A
// configuration that cannot work is refused immediately and left visible to
// browserPaneSpec instead of being silently ignored.
func ConfigureEmbeddedBrowser(executable string) error {
	exe := strings.TrimSpace(executable)
	embeddedBrowserMu.Lock()
	defer embeddedBrowserMu.Unlock()
	embeddedBrowserExecutable, embeddedBrowserErr = "", nil
	if exe == "" {
		return nil
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		embeddedBrowserErr = fmt.Errorf(
			"golder cannot start its own browser pane on %s: the browser draws into a tuios pane, which needs a Unix terminal; install kitty-browse for that platform instead",
			runtime.GOOS)
		return embeddedBrowserErr
	}
	abs, err := filepath.Abs(exe)
	if err != nil {
		embeddedBrowserErr = fmt.Errorf("resolve %q as the browser executable: %w", exe, err)
		return embeddedBrowserErr
	}
	info, err := os.Stat(abs)
	if err != nil {
		embeddedBrowserErr = fmt.Errorf("browser executable %s: %w", abs, err)
		return embeddedBrowserErr
	}
	if info.IsDir() || info.Mode().Perm()&0o111 == 0 {
		embeddedBrowserErr = fmt.Errorf("browser executable %s is not an executable file", abs)
		return embeddedBrowserErr
	}
	embeddedBrowserExecutable = abs
	return nil
}

// embeddedBrowser returns the configured executable, "" when the built-in pane
// is off, and the reason when the configuration was refused.
func embeddedBrowser() (string, error) {
	embeddedBrowserMu.RLock()
	defer embeddedBrowserMu.RUnlock()
	return embeddedBrowserExecutable, embeddedBrowserErr
}

// EmbeddedBrowserConfigured reports whether golder can start its own browser
// pane right now.
func EmbeddedBrowserConfigured() bool {
	exe, err := embeddedBrowser()
	return exe != "" && err == nil
}

// BrowserLaunchAvailable reports whether this process can start a browser pane
// at all: its own `browse` when main configured one, or an external
// kitty-browse. Registration uses it, so the tool appears without kitty-browse
// on PATH once the built-in pane is configured.
func BrowserLaunchAvailable() bool {
	if EmbeddedBrowserConfigured() {
		return true
	}
	_, err := exec.LookPath("kitty-browse")
	return err == nil
}

// KittyBrowseAvailable keeps the name existing callers use. It now answers for
// both launch programs, because "can a browser be started here" is the question
// callers actually have.
func KittyBrowseAvailable() bool { return BrowserLaunchAvailable() }

// browserPaneSpec is the pane command for a browser named name, pointed at url.
// The socket and profile live under the workspace: the profile keeps the login
// state across restarts, and the socket path is discoverable without config.
func browserPaneSpec(cwd, name, url string) (paneSpec, error) {
	if err := checkBrowserName(name); err != nil {
		return paneSpec{}, err
	}
	dir := browserStateDir(cwd)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return paneSpec{}, err
	}
	// The directory holds profiles with live sessions, so it is owner-only even
	// when it already existed with a looser mode.
	if err := os.Chmod(dir, 0o700); err != nil {
		return paneSpec{}, fmt.Errorf("browser state directory %s must be owner-only: %w", dir, err)
	}
	pruneBrowserProfiles(dir, name, time.Now())
	profile := browserProfileDir(cwd, name)
	if err := os.MkdirAll(profile, 0o700); err != nil {
		return paneSpec{}, err
	}
	if err := os.Chmod(profile, 0o700); err != nil {
		return paneSpec{}, fmt.Errorf("browser profile %s holds logins and must be owner-only: %w", profile, err)
	}
	socket, err := browserControlSocketPath(cwd, name)
	if err != nil {
		return paneSpec{}, err
	}
	if _, err := os.Lstat(socket); err == nil {
		if staleBrowserSocket(socket) {
			if err := os.Remove(socket); err != nil && !os.IsNotExist(err) {
				return paneSpec{}, fmt.Errorf("remove stale browser socket: %w", err)
			}
		} else {
			// A live (or unprobeable) endpoint keeps its path; give the new
			// instance its own socket instead of unlinking a possibly busy one.
			var suffix [4]byte
			if _, err := rand.Read(suffix[:]); err != nil {
				return paneSpec{}, err
			}
			socket = strings.TrimSuffix(socket, ".sock") + fmt.Sprintf("-%x.sock", suffix)
			if len(socket) > unixSocketPathLimit {
				return paneSpec{}, fmt.Errorf("browser control socket %s would exceed the %d-byte Unix socket path limit; stop the browser already using %s or move the workspace to a shorter path",
					socket, unixSocketPathLimit, dir)
			}
		}
	} else if !os.IsNotExist(err) {
		return paneSpec{}, err
	}
	argv, err := browserPaneArgv(socket, profile, url)
	if err != nil {
		return paneSpec{}, err
	}
	return paneSpec{
		Name: name, Kind: "browser", Socket: socket, URL: url,
		Embedded: EmbeddedBrowserConfigured(), Argv: argv,
	}, nil
}

// browserPaneArgv is the command line for one browser pane. golder's own binary
// gets a subcommand; an external kitty-browse takes the same flags directly.
func browserPaneArgv(socket, profile, url string) ([]string, error) {
	if exe, err := embeddedBrowser(); err != nil {
		return nil, err
	} else if exe != "" {
		return []string{exe, "browse", "-profile-dir", profile, "-control-socket", socket, url}, nil
	}
	bin, err := exec.LookPath("kitty-browse")
	if err != nil {
		return nil, fmt.Errorf("no browser can be started here: %s", kittyBrowseHint())
	}
	return []string{bin, "-profile-dir", profile, "-control-socket", socket, url}, nil
}

func staleBrowserSocket(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return false
	}
	conn, err := net.DialTimeout("unix", path, 200*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		return false
	}
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT)
}

// pruneBrowserProfiles removes profiles from browser names that have not been
// used for a while. The active name is always kept: tests and ad-hoc debugging
// otherwise leave one full Chromium profile per name under .golder/browser.
func pruneBrowserProfiles(dir, keep string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasSuffix(entry.Name(), ".profile") {
			continue
		}
		if entry.Name() == keep+".profile" {
			continue
		}
		info, err := entry.Info()
		if err != nil || now.Sub(info.ModTime()) < browserProfileMaxAge {
			continue
		}
		_ = os.RemoveAll(filepath.Join(dir, entry.Name()))
	}
}

// startBrowserPane opens a pane running a browser and adopts the socket it
// creates. It returns the socket and the pane id.
func startBrowserPane(ctx context.Context, cwd, name, url string, dial func(context.Context) (*tuiosctl.Client, error)) (string, string, error) {
	spec, err := browserPaneSpec(cwd, name, url)
	if err != nil {
		return "", "", err
	}
	c, err := dial(ctx)
	if err != nil {
		return "", "", err
	}
	defer c.Close()
	res, err := c.NewWindowInCallerWorkspace(ctx, tuiosctl.NewWindowParams{
		Name: spec.Name, Cwd: cwd, Command: spec.Argv, Focus: false, CloseOnExit: true,
	})
	if err != nil {
		return "", "", fmt.Errorf("start %s: %w", spec.program(), err)
	}
	if _, err := adoptBrowserPane(ctx, cwd, spec, res.WindowID, browserSocketWait); err != nil {
		return "", res.WindowID, err
	}
	return spec.Socket, res.WindowID, nil
}

// adoptBrowserPane turns "a pane was created" into "a browser we can prove the
// identity of". It waits for the control socket, proves the endpoint is a
// compatible browser, and records the exact pane id and browserId, which is
// what a later close needs before it sends a quit.
func adoptBrowserPane(ctx context.Context, cwd string, spec paneSpec, windowID string, wait time.Duration) (browserPaneRecord, error) {
	if err := waitForBrowserSocket(ctx, spec.Socket, wait); err != nil {
		return browserPaneRecord{}, fmt.Errorf(
			"%s is running in pane %s but %w; check the pane output and that Chromium is installed",
			spec.program(), windowID, err)
	}
	cBrowser, err := browserctl.Dial(spec.Socket)
	if err != nil {
		return browserPaneRecord{}, err
	}
	defer cBrowser.CloseIdleConnections()
	probeCtx, cancel := context.WithTimeout(ctx, browserProbeTimeout)
	defer cancel()
	info, err := cBrowser.Info(probeCtx)
	if err != nil {
		return browserPaneRecord{}, fmt.Errorf("new browser control endpoint is not ready: %w", err)
	}
	if info.APIVersion != browserctl.APIVersion || info.BrowserID == "" {
		return browserPaneRecord{}, fmt.Errorf("new control endpoint is not a compatible %s browser", browserctl.APIVersion)
	}
	record := browserPaneRecord{
		Root: cwd, Name: spec.Name, WindowID: windowID, Socket: spec.Socket,
		Program: spec.baseName(), BrowserID: info.BrowserID, Embedded: spec.Embedded,
	}
	rememberBrowserPane(record)
	// The metadata is what discovery and a later session read. Losing it is not
	// fatal for this session, so it does not fail the pane.
	_ = writeBrowserMetadata(browserMetadataPath(cwd, spec.Name), browserMetadata{
		Name: spec.Name, Socket: spec.Socket, Profile: browserProfileDir(cwd, spec.Name),
		WindowID: windowID, Program: record.Program, BrowserID: info.BrowserID, Embedded: spec.Embedded,
	})
	setBrowserSocket(spec.Socket)
	return record, nil
}

// waitForBrowserSocket waits for the control socket to appear, bounded by both
// the budget and the caller's context. The pane command can exit immediately
// (no Chromium, a bad flag), so a caller that is cancelled or has a shorter
// deadline must not be held for the full budget.
func waitForBrowserSocket(ctx context.Context, path string, d time.Duration) error {
	if d <= 0 {
		d = browserSocketWait
	}
	deadline := time.Now().Add(d)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if info, err := os.Stat(path); err == nil && info.Mode()&os.ModeSocket != 0 {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("its control socket %s did not appear within %s", path, d)
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// kittyBrowseHint points at a built binary the PATH missed, because the common
// case is a checkout that was built but never linked.
func kittyBrowseHint() string {
	var candidates []string
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".local", "bin", "kitty-browse"))
	}
	candidates = append(candidates,
		filepath.Join("/usr/local/bin", "kitty-browse"),
		filepath.Join("/opt/homebrew/bin", "kitty-browse"))
	for _, p := range candidates {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return fmt.Sprintf("found %s; add %s to PATH", p, filepath.Dir(p))
		}
	}
	return "install it with the release installer, or symlink it into a directory on PATH"
}
