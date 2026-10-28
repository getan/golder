//go:build (darwin || linux) && tuios_welcome_io_fix

package workspace

import (
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// A home-directory preset hint is optional UI decoration, not permission to
// block the welcome screen's render/input goroutine on an external file.
// Enable with -tags=tuios_welcome_io_fix against the patched dependency.
// Remove the extra build tag once go.mod pins a release containing that fix.
func TestE2ESSHWorkspaceWelcomeDoesNotOpenPipeConfig(t *testing.T) {
	bin := golderBinary(t)
	root := tempRoot(t)
	dir := project(t, root)
	env := append(isolatedEnv(t, root), "SSH_TTY=/dev/pts/fixture", "SSH_CONNECTION=192.0.2.1 1234 192.0.2.2 22")
	if err := unix.Mkfifo(filepath.Join(root, "home", ".tmux.conf"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopQuietly(bin, dir, env, "-s", "slow-config") })
	v := newTerminal(t).start(t, bin, dir, env, "workspace", "--shell", "-s", "slow-config", "--socket", filepath.Join(root, "s.sock"))
	v.waitForOutput(t, "\x1b[?1049h", 5*time.Second, "first workspace frame with pipe config")
	v.typeKeys(t, "\x1b2")
	v.waitForOutput(t, "AGENT WORKSPACE", 2*time.Second, "welcome with pipe config")
	v.typeKeys(t, "?")
	v.waitForOutput(t, "Keybindings", 2*time.Second, "outer help with pipe config")
	// The test's client/process/daemon cleanup owns every path involved. No
	// writer is needed to unstick a bad read; failing the test kills its client.
}
