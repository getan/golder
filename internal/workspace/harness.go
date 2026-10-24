package workspace

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"

	"github.com/getan/golder/internal/cli/workspacecmd"
	"github.com/getan/golder/internal/hostcompat"
	"github.com/getan/golder/internal/privatepath"
)

// writeHarness installs only into this daemon's owned state. The external
// host-compat installer remains opt-in and never runs on this path.
func (b *Backend) writeHarness(stateDir string) (string, error) {
	command, err := b.initialCommand(workspacecmd.Options{})
	if err != nil {
		return "", err
	}
	var manifest map[string]any
	if _, err := toml.Decode(hostcompat.ManifestSource(), &manifest); err != nil {
		return "", fmt.Errorf("workspace: decode bundled golder harness: %w", err)
	}
	manifest["resume"] = map[string]any{
		"argv":   []string{command[0], "--resume", "{session_id}"},
		"source": "resume through the same golder executable that owns this workspace",
	}
	var body bytes.Buffer
	if err := toml.NewEncoder(&body).Encode(manifest); err != nil {
		return "", fmt.Errorf("workspace: encode own harness: %w", err)
	}
	dir := filepath.Join(stateDir, "harnesses")
	if err := privatepath.EnsureDir(dir); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "golder.toml")
	if _, err := os.Lstat(path); err == nil {
		if err := privatepath.CheckFile(path, 0o600); err != nil {
			return "", err
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	f, err := os.CreateTemp(dir, ".golder-harness-")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(body.Bytes()); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return "", err
	}
	return dir, nil
}
