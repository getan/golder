package computersetup

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getan/golder/internal/computeruse"
)

type fixtureBackend struct {
	targets  []computeruse.Target
	apps     []computeruse.Application
	launched []computeruse.Application
	closed   atomic.Int32
	reject   error
}

func (f *fixtureBackend) Targets(context.Context) ([]computeruse.Target, error) {
	return f.targets, nil
}

func (f *fixtureBackend) Validate(context.Context, computeruse.Target, bool) error { return f.reject }

func (f *fixtureBackend) Close() error {
	f.closed.Add(1)
	return nil
}

func (f *fixtureBackend) closedCount() int { return int(f.closed.Load()) }

func (f *fixtureBackend) Applications(context.Context, string) ([]computeruse.Application, error) {
	return f.apps, nil
}

func (f *fixtureBackend) LaunchApplication(context.Context, computeruse.Application) error {
	return nil
}

func newFixture(t *testing.T, opts Options, backend *fixtureBackend) *Setup {
	t.Helper()
	opts.Backend = func() (computeruse.PlatformBackend, error) { return backend, nil }
	return New(opts)
}

func TestResolveAutoSelectsUniqueRequestedWindow(t *testing.T) {
	backend := &fixtureBackend{targets: []computeruse.Target{{ID: "main", Application: "Calculator", Window: "Main"}}}
	setup := newFixture(t, Options{SessionID: "s", Suggested: "Calculator"}, backend)
	if err := setup.Resolve(context.Background()); err != nil {
		t.Fatal(err)
	}
	view := setup.View()
	if view.Selected != 0 || !view.Matched || view.PreviewGate {
		t.Fatalf("view = %+v", view)
	}
	if plan := setup.Plan(); plan.Action != computeruse.SetupStartTarget || plan.TargetIndex != 0 {
		t.Fatalf("plan = %+v", plan)
	}
	if grant := setup.AutoGrant(); !grant.Control || grant.SemanticOnly {
		t.Fatalf("grant = %+v", grant)
	}
}

func TestResolveKeepsAmbiguousWindowsUnselected(t *testing.T) {
	backend := &fixtureBackend{targets: []computeruse.Target{
		{ID: "one", Application: "Calculator", Window: "Main"},
		{ID: "two", Application: "Calculator", Window: "Main"},
	}}
	setup := newFixture(t, Options{SessionID: "s", Suggested: "Calculator"}, backend)
	if err := setup.Resolve(context.Background()); err != nil {
		t.Fatal(err)
	}
	if view := setup.View(); view.Selected != -1 {
		t.Fatalf("ambiguous windows were preselected: %+v", view)
	}
	if plan := setup.Plan(); plan.Action != computeruse.SetupChooseWindow || len(plan.Matches) != 2 {
		t.Fatalf("plan = %+v", plan)
	}
	if !setup.Select(1) {
		t.Fatal("explicit selection was refused")
	}
	if target, ok := setup.Target(); !ok || target.ID != "two" {
		t.Fatalf("target = %+v, %v", target, ok)
	}
}

func TestResolveReportsMissingApplication(t *testing.T) {
	setup := newFixture(t, Options{SessionID: "s", Suggested: "Nope"}, &fixtureBackend{})
	if err := setup.Resolve(context.Background()); err != nil {
		t.Fatal(err)
	}
	view := setup.View()
	if !view.NotFound || view.Matched || view.Selected != -1 {
		t.Fatalf("view = %+v", view)
	}
	if plan := setup.Plan(); plan.Action != computeruse.SetupNoMatch {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestAmbiguousApplicationsNeedAnExplicitChoice(t *testing.T) {
	backend := &fixtureBackend{apps: []computeruse.Application{
		{ID: "one", Name: "Calculator", Identity: "/Applications/Calculator.app"},
		{ID: "two", Name: "Calculator", Identity: "/System/Applications/Calculator.app"},
	}}
	setup := newFixture(t, Options{SessionID: "s", Suggested: "Calculator"}, backend)
	if err := setup.Resolve(context.Background()); err != nil {
		t.Fatal(err)
	}
	if view := setup.View(); len(view.Applications) != 2 || view.Selected != -1 {
		t.Fatalf("view = %+v", view)
	}
	if plan := setup.Plan(); plan.Action != computeruse.SetupChooseApplication {
		t.Fatalf("plan = %+v", plan)
	}
	if err := setup.ChooseApplication(context.Background(), 5); !errors.Is(err, ErrNoTarget) {
		t.Fatalf("out-of-range choice = %v", err)
	}
}

func TestStartRequiresASelection(t *testing.T) {
	setup := newFixture(t, Options{SessionID: "s"}, &fixtureBackend{targets: []computeruse.Target{{ID: "one"}}})
	if err := setup.Resolve(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Start(context.Background(), Grant{Control: true}); !errors.Is(err, ErrNoTarget) {
		t.Fatalf("start without selection = %v", err)
	}
}

func TestStartRefusesControlWhenReadOnly(t *testing.T) {
	backend := &fixtureBackend{targets: []computeruse.Target{{ID: "one", Application: "Calculator"}}}
	setup := newFixture(t, Options{SessionID: "s", Suggested: "Calculator", ReadOnly: true}, backend)
	if err := setup.Resolve(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Start(context.Background(), Grant{Control: true}); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("read-only control = %v", err)
	}
	if backend.closedCount() != 0 {
		t.Fatal("refused start closed the catalog it still owns")
	}
}

func TestStartTransfersBackendOwnership(t *testing.T) {
	backend := &fixtureBackend{targets: []computeruse.Target{{ID: "one", Application: "Calculator"}}}
	setup := newFixture(t, Options{SessionID: "s", Suggested: "Calculator"}, backend)
	if err := setup.Resolve(context.Background()); err != nil {
		t.Fatal(err)
	}
	session, err := setup.Start(context.Background(), setup.AutoGrant())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if !session.Status().Active {
		t.Fatal("session did not start")
	}
	setup.Close()
	if backend.closedCount() != 0 {
		t.Fatal("closing a started setup closed the session's catalog")
	}
}

func TestFailedStartKeepsTheCatalogForRetry(t *testing.T) {
	backend := &fixtureBackend{
		targets: []computeruse.Target{{ID: "one", Application: "Calculator"}},
		reject:  errors.New("window vanished"),
	}
	setup := newFixture(t, Options{SessionID: "s", Suggested: "Calculator"}, backend)
	if err := setup.Resolve(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Start(context.Background(), setup.AutoGrant()); err == nil {
		t.Fatal("start succeeded with a rejected window")
	}
	if backend.closedCount() != 0 {
		t.Fatal("a failed start released the catalog the setup still owns")
	}
	setup.Close()
	deadline := time.Now().Add(2 * time.Second)
	for backend.closedCount() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if backend.closedCount() != 1 {
		t.Fatalf("catalog closed %d times", backend.closedCount())
	}
}

func TestCloseReleasesTheCatalogOnce(t *testing.T) {
	backend := &fixtureBackend{}
	setup := newFixture(t, Options{SessionID: "s", Suggested: "Calculator"}, backend)
	if err := setup.Resolve(context.Background()); err != nil {
		t.Fatal(err)
	}
	setup.Close()
	setup.Close()
	// Close releases asynchronously so a frontend's UI thread never blocks on
	// the platform catalog.
	deadline := time.Now().Add(2 * time.Second)
	for backend.closedCount() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if backend.closedCount() != 1 {
		t.Fatalf("catalog closed %d times", backend.closedCount())
	}
	if err := setup.Resolve(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("resolve after close = %v", err)
	}
}

func TestResolveRunsOnlyOnce(t *testing.T) {
	backend := &fixtureBackend{targets: []computeruse.Target{{ID: "one", Application: "Calculator"}}}
	setup := newFixture(t, Options{SessionID: "s", Suggested: "Calculator"}, backend)
	if err := setup.Resolve(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := setup.Resolve(context.Background()); !errors.Is(err, ErrResolved) {
		t.Fatalf("second resolve = %v", err)
	}
}
