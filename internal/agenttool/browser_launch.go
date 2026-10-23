package agenttool

// Starting kitty-browse used to be three things a person had to remember: a
// profile directory, a control socket under the workspace, and the export that
// tells golder where the socket is. None of that is a decision — it is setup.
// The agent runs it itself: inside a tuios pane it opens a pane with the right
// argv, waits for the socket, and adopts it for the session.

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/getan/golder/internal/browserctl"
	"github.com/getan/golder/internal/tuiosctl"
)

// KittyBrowseAvailable reports whether the binary is installed. A browser can
// only be started where it can draw, so this is checked together with the pane.
func KittyBrowseAvailable() bool {
	_, err := exec.LookPath("kitty-browse")
	return err == nil
}

// browserPaneSpec is the pane command for a browser named name, pointed at url.
// The socket and profile live under the workspace: the profile keeps the login
// state across restarts, and the socket path is discoverable without config.
func browserPaneSpec(cwd, name, url string) (paneSpec, error) {
	bin, err := exec.LookPath("kitty-browse")
	if err != nil {
		return paneSpec{}, fmt.Errorf("kitty-browse is not on PATH; %s", kittyBrowseHint())
	}
	dir := filepath.Join(cwd, ".golder", "browser")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return paneSpec{}, err
	}
	socket := filepath.Join(dir, name+".sock")
	if _, err := os.Lstat(socket); err == nil {
		// Never unlink an endpoint on a failed health probe: it could belong to
		// a live but busy browser. A fresh instance gets its own endpoint.
		var suffix [4]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			return paneSpec{}, err
		}
		socket = filepath.Join(dir, fmt.Sprintf("%s-%x.sock", name, suffix))
	} else if !os.IsNotExist(err) {
		return paneSpec{}, err
	}
	profile := filepath.Join(dir, name+".profile")
	return paneSpec{
		Name: name, Kind: "browser", Socket: socket, URL: url,
		Argv: []string{bin, "-profile-dir", profile, "-control-socket", socket, url},
	}, nil
}

// startBrowserPane opens a pane running kitty-browse and adopts the socket it
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
		return "", "", fmt.Errorf("start kitty-browse: %w", err)
	}
	if !waitForSocket(spec.Socket, 25*time.Second) {
		return "", res.WindowID, fmt.Errorf(
			"kitty-browse is running in pane %s but %s did not appear; check that Chromium is installed",
			res.WindowID, spec.Socket)
	}
	cBrowser, err := browserctl.Dial(spec.Socket)
	if err != nil {
		return "", res.WindowID, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	defer cBrowser.CloseIdleConnections()
	info, err := cBrowser.Info(probeCtx)
	if err != nil {
		return "", res.WindowID, fmt.Errorf("new browser control endpoint is not ready: %w", err)
	}
	if info.APIVersion != browserctl.APIVersion || info.BrowserID == "" {
		return "", res.WindowID, fmt.Errorf("new control endpoint is not a compatible browser")
	}
	setBrowserSocket(spec.Socket)
	return spec.Socket, res.WindowID, nil
}

func waitForSocket(path string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if info, err := os.Stat(path); err == nil && info.Mode()&os.ModeSocket != 0 {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
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
