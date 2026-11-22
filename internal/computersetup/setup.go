// Package computersetup owns the interactive "open a desktop application for
// Computer Use" flow that the TUI and the GUI must share: resolve a requested
// application, choose one window, take the grant, and start the session.
//
// The package holds no rendering and no terminal/UI dependency. Frontends
// render View/Plan and call these methods from their own goroutine or message
// loop, so the decision about which application and window to use can only be
// made here.
package computersetup

import (
	"context"
	"errors"
	"runtime"
	"sync"

	"github.com/getan/golder/internal/computeruse"
)

var (
	// ErrClosed reports a setup that was already dismissed by the frontend.
	ErrClosed = errors.New("computer setup is no longer current")
	// ErrResolved reports a second resolution of the same setup.
	ErrResolved = errors.New("computer setup was already resolved")
	// ErrNoTarget reports a start without a selected window.
	ErrNoTarget = errors.New("no desktop window is selected")
	// ErrReadOnly reports a control grant while the conversation is read-only.
	ErrReadOnly = errors.New("computer control is unavailable in read-only mode")
)

// Options describe one setup. Suggested is the application name the model
// requested; it is empty for a manually opened picker.
type Options struct {
	SessionID string
	Suggested string
	ReadOnly  bool
	// Guard revalidates the frontend's live authorization immediately before an
	// asynchronous application launch. It must not select a window.
	Guard func() error
	// Backend overrides the platform backend. Tests use it; production leaves
	// it nil so every setup gets a fresh catalog.
	Backend func() (computeruse.PlatformBackend, error)
}

// Grant is the authority a frontend asks for.
type Grant struct {
	Control bool
	// SemanticOnly skips the operator preview whose confirmation proves a
	// system-selected capture source. Backends without a preview gate ignore it.
	SemanticOnly bool
	// SkipPreview skips only the confirmation step, keeping capture and control.
	// Used when the person explicitly chose the window on macOS.
	SkipPreview bool
}

// View is the resolution state a frontend renders.
type View struct {
	Targets      []computeruse.Target
	Applications []computeruse.Application
	Matched      bool
	NotFound     bool
	Selected     int
	Suggested    string
	PreviewGate  bool
}

// Setup is one application/window selection. Backends are owned here until a
// session takes them, and it is safe to call Close from any goroutine.
type Setup struct {
	mu           sync.Mutex
	opts         Options
	backend      computeruse.PlatformBackend
	targets      []computeruse.Target
	applications []computeruse.Application
	matched      bool
	notFound     bool
	selected     int
	closed       bool
}

// New prepares an unresolved setup. Selected starts at -1 so a frontend can
// rely on "no window chosen yet" before Resolve runs.
func New(opts Options) *Setup {
	return &Setup{opts: opts, selected: -1}
}

// SessionID reports the conversation this setup belongs to.
func (s *Setup) SessionID() string { return s.opts.SessionID }

// Suggested reports the requested application name, if any.
func (s *Setup) Suggested() string { return s.opts.Suggested }

// ReadOnly reports the permission mode captured when the setup was created.
func (s *Setup) ReadOnly() bool { return s.opts.ReadOnly }

// View returns a copy of the current resolution state.
func (s *Setup) View() View {
	s.mu.Lock()
	defer s.mu.Unlock()
	return View{
		Targets:      append([]computeruse.Target(nil), s.targets...),
		Applications: append([]computeruse.Application(nil), s.applications...),
		Matched:      s.matched,
		NotFound:     s.notFound,
		Selected:     s.selected,
		Suggested:    s.opts.Suggested,
		PreviewGate:  s.previewGateLocked(),
	}
}

// Plan is the shared decision every frontend renders: installed applications
// first, then no match, then a unique requested window, then the window picker.
func (s *Setup) Plan() computeruse.SetupPlan {
	s.mu.Lock()
	resolution := computeruse.ApplicationResolution{
		Targets:      append([]computeruse.Target(nil), s.targets...),
		Applications: append([]computeruse.Application(nil), s.applications...),
		Matched:      s.matched,
		NotFound:     s.notFound,
	}
	s.mu.Unlock()
	return computeruse.PlanApplicationSetup(resolution, s.opts.Suggested)
}

// Resolve discovers or launches the requested application. It must run once;
// repeating it would let a conversation retarget itself after granting access.
func (s *Setup) Resolve(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	if s.backend != nil || len(s.targets) > 0 || len(s.applications) > 0 {
		s.mu.Unlock()
		return ErrResolved
	}
	factory := s.opts.Backend
	s.mu.Unlock()
	if factory == nil {
		factory = computeruse.NewPlatformBackend
	}
	backend, err := factory()
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		go backend.Close()
		return err
	}
	launchCtx := ctx
	if s.opts.Guard != nil {
		launchCtx = computeruse.WithApplicationLaunchGuard(ctx, s.opts.Guard)
	}
	resolution, err := computeruse.ResolveApplication(launchCtx, backend, s.opts.Suggested)
	if err != nil {
		go backend.Close()
		return err
	}
	return s.adopt(backend, resolution)
}

// ChooseApplication launches one installed candidate and adopts its windows.
func (s *Setup) ChooseApplication(ctx context.Context, index int) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	backend := s.backend
	if backend == nil || index < 0 || index >= len(s.applications) {
		s.mu.Unlock()
		return ErrNoTarget
	}
	app := s.applications[index]
	s.mu.Unlock()
	launchCtx := ctx
	if s.opts.Guard != nil {
		launchCtx = computeruse.WithApplicationLaunchGuard(ctx, s.opts.Guard)
	}
	resolution, err := computeruse.LaunchAndFindApplication(launchCtx, backend, app)
	if err != nil {
		return err
	}
	return s.adopt(backend, resolution)
}

// adopt stores a resolution and applies the shared auto-selection rule.
func (s *Setup) adopt(backend computeruse.PlatformBackend, resolution computeruse.ApplicationResolution) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		if backend != s.backend {
			go backend.Close()
		}
		return ErrClosed
	}
	previous := s.backend
	s.backend = backend
	if previous != nil && previous != backend {
		go previous.Close()
	}
	s.targets = resolution.Targets
	s.applications = resolution.Applications
	s.matched, s.notFound = resolution.Matched, resolution.NotFound
	s.selected = -1
	if s.opts.Suggested != "" && len(resolution.Applications) == 0 {
		if plan := computeruse.PlanApplicationSetup(resolution, s.opts.Suggested); plan.Action == computeruse.SetupStartTarget {
			s.selected = plan.TargetIndex
		}
	}
	return nil
}

// Select records an explicit window choice. It refuses ambiguous or stale
// indexes so a frontend cannot start a window the person did not see.
func (s *Setup) Select(index int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || index < 0 || index >= len(s.targets) {
		return false
	}
	s.selected = index
	return true
}

// Target returns the selected window.
func (s *Setup) Target() (computeruse.Target, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.selected < 0 || s.selected >= len(s.targets) {
		return computeruse.Target{}, false
	}
	return s.targets[s.selected], true
}

// AutoGrant is the authority a requested application receives after a unique
// match: control, with the operator preview skipped only when the backend
// requires that confirmation for a system-selected source.
func (s *Setup) AutoGrant() Grant {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Grant{Control: true, SemanticOnly: s.previewGateLocked() && runtime.GOOS != "darwin", SkipPreview: runtime.GOOS == "darwin"}
}

// HasPreviewGate reports whether the backend asks for a system-selected
// capture source that a person must confirm.
func (s *Setup) HasPreviewGate() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.previewGateLocked()
}

func (s *Setup) previewGateLocked() bool {
	_, ok := s.backend.(computeruse.OperatorPreviewBackend)
	return ok
}

// PreparePreview asks the backend for an operator-only preview of the selected
// window. It never starts a session.
func (s *Setup) PreparePreview(ctx context.Context) (computeruse.Target, computeruse.OperatorPreview, error) {
	s.mu.Lock()
	backend, target, ok := s.backend, computeruse.Target{}, false
	if s.selected >= 0 && s.selected < len(s.targets) {
		target, ok = s.targets[s.selected], true
	}
	s.mu.Unlock()
	gate, hasGate := backend.(computeruse.OperatorPreviewBackend)
	if !hasGate || !ok {
		return computeruse.Target{}, computeruse.OperatorPreview{}, computeruse.ErrUnavailable
	}
	preview, err := gate.PrepareOperatorPreview(ctx, target)
	if err != nil {
		return target, computeruse.OperatorPreview{}, err
	}
	if preview.ID == "" {
		return target, computeruse.OperatorPreview{}, errors.New("operator preview did not provide a confirmation identity")
	}
	return target, preview, nil
}

// Start begins the selected window with the requested grant. It prepares and
// installs the operator preview unless the grant is semantic-only, and returns
// the active session; the backend then belongs to that session.
func (s *Setup) Start(ctx context.Context, grant Grant) (*computeruse.Session, error) {
	return s.start(ctx, grant, "")
}

// StartConfirmed is Start for a frontend that already showed the preview to the
// person and collected an explicit confirmation of the same window.
func (s *Setup) StartConfirmed(ctx context.Context, grant Grant, candidateID string) (*computeruse.Session, error) {
	if candidateID == "" {
		return nil, errors.New("computer use requires a confirmed preview identity")
	}
	return s.start(ctx, grant, candidateID)
}

func (s *Setup) start(ctx context.Context, grant Grant, candidateID string) (*computeruse.Session, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrClosed
	}
	backend := s.backend
	if backend == nil {
		s.mu.Unlock()
		return nil, computeruse.ErrUnavailable
	}
	if s.selected < 0 || s.selected >= len(s.targets) {
		s.mu.Unlock()
		return nil, ErrNoTarget
	}
	if grant.Control && s.opts.ReadOnly {
		s.mu.Unlock()
		return nil, ErrReadOnly
	}
	target := s.targets[s.selected]
	s.mu.Unlock()

	var preview computeruse.OperatorPreview
	if gate, ok := backend.(computeruse.OperatorPreviewBackend); ok && !grant.SemanticOnly && !grant.SkipPreview {
		var err error
		if candidateID != "" {
			err = gate.ConfirmOperatorPreview(ctx, target, candidateID)
		} else {
			preview, err = gate.PrepareOperatorPreview(ctx, target)
			if err == nil && preview.ID == "" {
				err = errors.New("operator preview did not provide a confirmation identity")
			}
		}
		if err != nil {
			return nil, err
		}
	}

	session := computeruse.New(backend)
	if err := session.Start(ctx, target, computeruse.Capabilities{Observe: true, Control: grant.Control}); err != nil {
		// The catalog is still the setup's to retry; only the failed session is
		// discarded.
		session.Stop()
		return nil, err
	}
	if preview.ID != "" {
		if err := session.InstallOperatorPreview(preview); err != nil {
			// The session already owns the catalog, so closing it releases both.
			_ = session.Close()
			s.mu.Lock()
			if s.backend == backend {
				s.backend = nil
			}
			s.mu.Unlock()
			return nil, err
		}
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = session.Close()
		return nil, ErrClosed
	}
	s.backend = nil // ownership moved to the session
	s.mu.Unlock()
	return session, nil
}

// Close releases the catalog if no session took it. Sessions are stopped by the
// frontend that owns them.
func (s *Setup) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	backend := s.backend
	s.backend = nil
	s.mu.Unlock()
	if backend != nil {
		go backend.Close()
	}
}
