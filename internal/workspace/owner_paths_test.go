//go:build darwin || linux

package workspace

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/cli/workspacecmd"
)

func TestOwnerEndpointFitsAValidDeepCustomSocket(t *testing.T) {
	// The CLI's 92-byte budget includes a one-byte primary basename. A fixed
	// 23-byte ownership basename used to fail only after starting the panes.
	dir := "/" + strings.Repeat("d", 89)
	opts := workspacecmd.Options{Socket: filepath.Join(dir, "s")}
	first, record := ownerPaths(opts)
	if len(opts.Socket) != 92 || len(first) > 103 {
		t.Fatalf("primary/owner path lengths = %d/%d", len(opts.Socket), len(first))
	}
	opts.Socket = filepath.Join(dir, "t")
	second, otherRecord := ownerPaths(opts)
	if first == second || record == otherRecord {
		t.Fatal("distinct primary sockets lost their ownership namespace")
	}
}
