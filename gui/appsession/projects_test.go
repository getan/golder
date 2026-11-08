package appsession

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/getan/golder/internal/golderhome"
	"github.com/getan/golder/internal/session"
)

func testProjectRoot(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return filepath.VolumeName(path) + string(os.PathSeparator)
}

func TestResolveGraphicalLaunchUsesSelectedProjectNotRoot(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	root := testProjectRoot(t)
	c := &Controller{}
	if _, err := c.AddProject(root); err != nil {
		t.Fatal(err)
	}
	first, err := c.AddProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.AddProject(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := c.SelectProject(first.Path); err != nil {
		t.Fatal(err)
	}
	got, fallback, err := ResolveLaunchProject(root, false)
	if err != nil || !fallback || got != first.Path {
		t.Fatalf("graphical launch = %q, fallback=%t, err=%v; want %q", got, fallback, err, first.Path)
	}
	if err := c.RemoveProject(root); err != nil {
		t.Fatal(err)
	}
	if err := ensureInitialProject(got); err != nil {
		t.Fatal(err)
	}
	projects, _, err := c.ListProjects()
	if err != nil {
		t.Fatal(err)
	}
	for _, project := range projects {
		if isFilesystemRoot(project.Path) {
			t.Fatalf("removed root was re-added: %+v", projects)
		}
	}
}

func TestResolveGraphicalLaunchSkipsSelectedRootAndStaleProjects(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	root := testProjectRoot(t)
	c := &Controller{}
	project, err := c.AddProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.AddProject(root); err != nil {
		t.Fatal(err)
	}
	got, fallback, err := ResolveLaunchProject(root, false)
	if err != nil || !fallback || got != project.Path {
		t.Fatalf("selected root fallback = %q, %t, %v", got, fallback, err)
	}
	if err := ensureInitialProject(got); err != nil {
		t.Fatal(err)
	}
	if err := c.SelectProject(got); err != nil {
		t.Fatal(err)
	}
	if _, selected, err := c.ListProjects(); err != nil || selected != project.Path {
		t.Fatalf("fallback did not become the selected project: %q, %v", selected, err)
	}
	if err := os.RemoveAll(project.Path); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	want, err := canonicalProjectPath(home)
	if err != nil {
		t.Fatal(err)
	}
	got, fallback, err = ResolveLaunchProject(root, false)
	if err != nil || !fallback || got != want {
		t.Fatalf("home fallback = %q, %t, %v; want %q", got, fallback, err, want)
	}
}

func TestResolveLaunchPreservesExplicitRootAndNormalDirectory(t *testing.T) {
	root := testProjectRoot(t)
	for _, tc := range []struct {
		path     string
		explicit bool
	}{
		{root, true},
		{t.TempDir(), false},
	} {
		got, fallback, err := ResolveLaunchProject(tc.path, tc.explicit)
		if err != nil || fallback || got != tc.path {
			t.Errorf("launch %q (explicit=%t) = %q, %t, %v", tc.path, tc.explicit, got, fallback, err)
		}
	}
}

func TestGraphicalLaunchDoesNotFallbackToRootHome(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	root := testProjectRoot(t)
	t.Setenv("HOME", root)
	t.Setenv("USERPROFILE", root)
	got, fallback, err := ResolveLaunchProject(root, false)
	if err == nil || fallback || got != "" {
		t.Fatalf("root home fallback = %q, %t, %v", got, fallback, err)
	}
}

func TestProjectRegistryAndSessionDeletionAreIndependent(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	root := testProjectRoot(t)
	c := &Controller{}
	if _, err := c.AddProject(root); err != nil {
		t.Fatal(err)
	}
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(session.SessionHeader{ID: "first", Cwd: root}, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete("first"); err != nil {
		t.Fatal(err)
	}
	projects, _, err := c.ListProjects()
	if err != nil || len(projects) != 1 || !isFilesystemRoot(projects[0].Path) {
		t.Fatalf("deleting a session unexpectedly removed the project: %+v, %v", projects, err)
	}
	if err := store.Save(session.SessionHeader{ID: "second", Cwd: root}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveProject(root); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Load("second"); err != nil {
		t.Fatalf("removing a project deleted a session: %v", err)
	}
}

func TestResolveGraphicalLaunchPreservesDamagedRegistry(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("GOLDER_HOME", stateDir)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	path := filepath.Join(stateDir, golderhome.GUIProjectsFile)
	const damaged = "{invalid project registry"
	if err := os.WriteFile(path, []byte(damaged), 0o600); err != nil {
		t.Fatal(err)
	}
	want, err := canonicalProjectPath(home)
	if err != nil {
		t.Fatal(err)
	}
	got, fallback, err := ResolveLaunchProject(testProjectRoot(t), false)
	if err != nil || !fallback || got != want {
		t.Fatalf("damaged registry fallback = %q, %t, %v; want %q", got, fallback, err, want)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != damaged {
		t.Fatalf("launch changed the damaged registry: %q, %v", data, err)
	}
}
