package computeruse

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Application is a locally discovered, opaque launch candidate. Its ID is
// valid only for the backend that discovered it; Detail is for disambiguation.
type Application struct {
	ID       string
	Name     string
	Identity string
	Detail   string
}

// ApplicationLauncher is an optional platform capability. Names are matched
// against local inventory, never interpreted as paths or shell commands.
type ApplicationLauncher interface {
	Applications(context.Context, string) ([]Application, error)
	LaunchApplication(context.Context, Application) error
}

type launchGuardKey struct{}

// WithApplicationLaunchGuard rechecks a frontend's live authorization just
// before a discovered app is launched, after any asynchronous discovery.
func WithApplicationLaunchGuard(ctx context.Context, guard func() error) context.Context {
	return context.WithValue(ctx, launchGuardKey{}, guard)
}

// ApplicationResolution contains either matching windows, ambiguous installed
// applications, or other windows for an explicit operator choice.
type ApplicationResolution struct {
	Targets      []Target
	Applications []Application
	Matched      bool
}

// ResolveApplication first reuses matching discovered windows. If none exist, it
// launches only an unambiguous locally discovered application. Multiple app or
// window candidates are returned for a person to disambiguate.
func ResolveApplication(ctx context.Context, backend PlatformBackend, name string) (ApplicationResolution, error) {
	if backend == nil {
		return ApplicationResolution{}, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return ApplicationResolution{}, err
	}
	targets, err := backend.Targets(ctx)
	if err != nil {
		return ApplicationResolution{}, err
	}
	if err := ctx.Err(); err != nil {
		return ApplicationResolution{}, err
	}
	if strings.TrimSpace(name) == "" {
		return ApplicationResolution{Targets: targets}, nil
	}
	matches := MatchApplications(name, targets)
	if len(matches) > 0 {
		selected := make([]Target, 0, len(matches))
		for _, i := range matches {
			selected = append(selected, targets[i])
		}
		return ApplicationResolution{Targets: selected, Matched: true}, nil
	}
	launcher, ok := backend.(ApplicationLauncher)
	if !ok {
		return ApplicationResolution{Targets: targets}, nil
	}
	apps, err := launcher.Applications(ctx, name)
	if err != nil {
		return ApplicationResolution{}, err
	}
	if err := ctx.Err(); err != nil {
		return ApplicationResolution{}, err
	}
	if len(apps) > 1 {
		return ApplicationResolution{Applications: apps}, nil
	}
	if len(apps) == 0 {
		return ApplicationResolution{Targets: targets}, nil
	}
	// The requested localized name can differ from the window owner's name.
	// Reuse the exact catalogued application before asking the OS to open it.
	if apps[0].Identity != "" {
		var existing []Target
		for _, target := range targets {
			if target.ApplicationID == apps[0].Identity {
				existing = append(existing, target)
			}
		}
		if len(existing) > 0 {
			return ApplicationResolution{Targets: existing, Matched: true}, nil
		}
	}
	return LaunchAndFindApplication(ctx, backend, apps[0])
}

// LaunchAndFindApplication waits for a window belonging to the exact launched
// application. It never substitutes a window from another application.
func LaunchAndFindApplication(ctx context.Context, backend PlatformBackend, app Application) (ApplicationResolution, error) {
	launcher, ok := backend.(ApplicationLauncher)
	if !ok || app.ID == "" || app.Name == "" {
		return ApplicationResolution{}, errors.New("application launch is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return ApplicationResolution{}, err
	}
	if guard, ok := ctx.Value(launchGuardKey{}).(func() error); ok && guard != nil {
		if err := guard(); err != nil {
			return ApplicationResolution{}, err
		}
	}
	if err := launcher.LaunchApplication(ctx, app); err != nil {
		return ApplicationResolution{}, err
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(12 * time.Second)
	defer deadline.Stop()
	for {
		targets, err := backend.Targets(ctx)
		if err != nil {
			return ApplicationResolution{}, err
		}
		if err := ctx.Err(); err != nil {
			return ApplicationResolution{}, err
		}
		matches := make([]Target, 0, len(targets))
		for _, target := range targets {
			if app.Identity != "" && app.Identity == target.ApplicationID || app.Identity == "" && normalizeApplicationName(app.Name) == normalizeApplicationName(target.Application) {
				matches = append(matches, target)
			}
		}
		if len(matches) > 0 {
			return ApplicationResolution{Targets: matches, Matched: true}, nil
		}
		select {
		case <-ctx.Done():
			return ApplicationResolution{}, ctx.Err()
		case <-deadline.C:
			return ApplicationResolution{}, fmt.Errorf("open request for %s succeeded, but no matching window was discovered within 12 seconds; the application may not have created a usable window", app.Name)
		case <-ticker.C:
		}
	}
}
