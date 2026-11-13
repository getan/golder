// Package permissionruntime owns the permission bootstrap shared by local
// frontends. It is process-scoped, never a per-project/session factory hook.
package permissionruntime

import (
	"fmt"
	"os"
	"sync"

	"github.com/getan/golder/internal/grants"
	"github.com/getan/golder/internal/permissions"
)

type Report struct {
	Ready    bool
	Warnings []string
}

var initializeMu sync.Mutex

// Initialize loads the live table exactly once. An existing table belongs to
// the host; revisiting this seam must not discard its session grants/refusals.
// Failure is explicit: frontends must not build their tool runtime after it.
func Initialize(getenv func(string) string) (Report, error) {
	initializeMu.Lock()
	defer initializeMu.Unlock()
	if grants.Process() != nil {
		return Report{Ready: true}, nil
	}
	if getenv == nil {
		getenv = os.Getenv
	}
	warnings, err := grants.Open(getenv)
	if err != nil {
		return Report{Warnings: warnings}, fmt.Errorf("permission runtime unavailable: %w", err)
	}
	for _, root := range permissions.ReadableRootsFromEnv(getenv) {
		if _, err := grants.AddReadRoot(root, grants.Session, "GOLDER_READABLE_ROOTS"); err != nil {
			warnings = append(warnings, fmt.Sprintf("readable root %s: %v", root, err))
		}
	}
	return Report{Ready: true, Warnings: warnings}, nil
}
