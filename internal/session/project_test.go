package session

// Tests for the project identity that scopes session views: the .git walk, the
// derived id, and the same-project relation (repo subdirs match, unrelated
// trees and unresolvable paths do not).

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectRootFindsRepositoryRoot(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(repo, "internal", "cli")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	if got := ProjectRoot(sub); got != repo {
		t.Errorf("ProjectRoot(sub) = %q, want the repository root %q", got, repo)
	}
	if got := ProjectRoot(repo); got != repo {
		t.Errorf("ProjectRoot(repo) = %q, want %q", got, repo)
	}
}

// TestProjectRootWorktreeGitFile covers a linked worktree and a submodule,
// where .git is a file pointing at the real git dir rather than a directory.
func TestProjectRootWorktreeGitFile(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ".git"), []byte("gitdir: /elsewhere/.git/worktrees/wt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(repo, "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := ProjectRoot(sub); got != repo {
		t.Errorf("ProjectRoot(sub) = %q, want %q (the .git file marks the root)", got, repo)
	}
}

func TestProjectRootOutsideRepositoryIsItself(t *testing.T) {
	plain := t.TempDir()
	if got := ProjectRoot(plain); got != plain {
		t.Errorf("ProjectRoot(plain dir) = %q, want itself %q", got, plain)
	}
}

func TestProjectRootEmptyAndRelative(t *testing.T) {
	if got := ProjectRoot(""); got != "" {
		t.Errorf("ProjectRoot(\"\") = %q, want \"\"", got)
	}
	// A relative path resolves against the process working directory instead of
	// being hashed as a bare string, so it matches how sessions record their cwd.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if got := ProjectRoot("."); got != ProjectRoot(wd) {
		t.Errorf("ProjectRoot(.) = %q, want ProjectRoot(cwd) = %q", got, ProjectRoot(wd))
	}
}

func TestProjectIDSameForRepoAndSubdir(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	id, subID := ProjectID(repo), ProjectID(sub)
	if id == "" || id != subID {
		t.Errorf("ProjectID(repo) = %q, ProjectID(sub) = %q; want equal non-empty", id, subID)
	}
	if len(id) != 12 {
		t.Errorf("ProjectID length = %d, want 12", len(id))
	}
}

func TestSameProject(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(repo, "x")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()

	if !SameProject(repo, sub) {
		t.Error("a repository and its subdirectory must be the same project")
	}
	if SameProject(repo, other) {
		t.Error("unrelated directories must not be the same project")
	}
	if SameProject("", repo) || SameProject(repo, "") {
		t.Error("an unresolved directory must never match a project")
	}
}
