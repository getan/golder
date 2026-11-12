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

// ApplicationWindowLookup discovers windows of the exact catalogued app,
// including windows outside the foreground desktop when the OS can prove them.
// It never activates an application or expands the selected app identity.
type ApplicationWindowLookup interface {
	ApplicationWindows(context.Context, Application) ([]Target, error)
}

// ApplicationRevealer can bring an already catalogued application forward if a
// background launch did not produce a visible window. It must revalidate the
// same application identity, and must not select or control a window itself.
type ApplicationRevealer interface {
	RevealApplication(context.Context, Application) error
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
	if finder, ok := backend.(ApplicationWindowLookup); ok {
		existing, err := matchingApplicationWindows(ctx, finder, apps[0])
		if err != nil {
			return ApplicationResolution{}, err
		}
		if len(existing) > 0 {
			return ApplicationResolution{Targets: existing, Matched: true}, nil
		}
	}
	return LaunchAndFindApplication(ctx, backend, apps[0])
}

func matchingApplicationWindows(ctx context.Context, finder ApplicationWindowLookup, app Application) ([]Target, error) {
	if app.Identity == "" {
		return nil, nil
	}
	targets, err := finder.ApplicationWindows(ctx, app)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var matches []Target
	for _, target := range targets {
		if target.ApplicationID == app.Identity {
			matches = append(matches, target)
		}
	}
	return matches, nil
}

// LaunchAndFindApplication waits for a window belonging to the exact launched
// application. It never substitutes a window from another application.
func LaunchAndFindApplication(ctx context.Context, backend PlatformBackend, app Application) (ApplicationResolution, error) {
	return launchAndFindApplication(ctx, backend, app, time.Second)
}

func launchAndFindApplication(ctx context.Context, backend PlatformBackend, app Application, revealAfter time.Duration) (ApplicationResolution, error) {
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
	var reveal <-chan time.Time
	revealer, canReveal := backend.(ApplicationRevealer)
	_, canFindBackground := backend.(ApplicationWindowLookup)
	if canReveal && !canFindBackground {
		timer := time.NewTimer(revealAfter)
		defer timer.Stop()
		reveal = timer.C
	}
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
		if len(matches) == 0 && canFindBackground {
			matches, err = matchingApplicationWindows(ctx, backend.(ApplicationWindowLookup), app)
			if err != nil {
				return ApplicationResolution{}, err
			}
		}
		if len(matches) > 0 {
			return ApplicationResolution{Targets: matches, Matched: true}, nil
		}
		select {
		case <-ctx.Done():
			return ApplicationResolution{}, ctx.Err()
		case <-reveal:
			reveal = nil // Never repeatedly move the user's focus or spaces.
			if err := ctx.Err(); err != nil {
				return ApplicationResolution{}, err
			}
			if guard, ok := ctx.Value(launchGuardKey{}).(func() error); ok && guard != nil {
				if err := guard(); err != nil {
					return ApplicationResolution{}, err
				}
			}
			if err := ctx.Err(); err != nil {
				return ApplicationResolution{}, err
			}
			if err := revealer.RevealApplication(ctx, app); err != nil {
				return ApplicationResolution{}, err
			}
		case <-deadline.C:
			return ApplicationResolution{}, fmt.Errorf("open request for %s succeeded, but no matching usable window was discovered within 12 seconds; restore a minimized window or show it on the current desktop and retry", app.Name)
		case <-ticker.C:
		}
	}
}
