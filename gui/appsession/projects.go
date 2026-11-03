package appsession

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/getan/golder/internal/golderhome"
)

const projectFileVersion = 1

var projectFileMu sync.Mutex

// ProjectFolder is one folder the person explicitly added to the desktop
// sidebar. Sessions do not create project rows implicitly.
type ProjectFolder struct {
	Path       string    `json:"path"`
	Name       string    `json:"name"`
	AddedAt    time.Time `json:"added_at"`
	LastOpened time.Time `json:"last_opened"`
}

type projectFile struct {
	Version  int             `json:"version"`
	Selected string          `json:"selected,omitempty"`
	Projects []ProjectFolder `json:"projects"`
}

// ListProjects returns the explicit desktop project list, most recently opened
// first. It never derives rows from historical session Cwd values.
func (c *Controller) ListProjects() ([]ProjectFolder, string, error) {
	projectFileMu.Lock()
	defer projectFileMu.Unlock()
	state, err := loadProjectFile()
	if err != nil {
		return nil, "", err
	}
	projects := append([]ProjectFolder(nil), state.Projects...)
	sort.SliceStable(projects, func(i, j int) bool {
		return projects[i].LastOpened.After(projects[j].LastOpened)
	})
	return projects, state.Selected, nil
}

// AddProject validates and canonically records one operator-selected directory.
// It does not start an Agent or create a Session.
func (c *Controller) AddProject(path string) (ProjectFolder, error) {
	canonical, err := canonicalProjectPath(path)
	if err != nil {
		return ProjectFolder{}, err
	}
	projectFileMu.Lock()
	defer projectFileMu.Unlock()
	state, err := loadProjectFile()
	if err != nil {
		return ProjectFolder{}, err
	}
	now := time.Now().UTC()
	for i := range state.Projects {
		if state.Projects[i].Path == canonical {
			state.Projects[i].LastOpened = now
			state.Selected = canonical
			if err := writeProjectFile(state); err != nil {
				return ProjectFolder{}, err
			}
			return state.Projects[i], nil
		}
	}
	project := ProjectFolder{
		Path: canonical, Name: filepath.Base(canonical), AddedAt: now, LastOpened: now,
	}
	state.Projects = append(state.Projects, project)
	state.Selected = canonical
	if err := writeProjectFile(state); err != nil {
		return ProjectFolder{}, err
	}
	return project, nil
}

// SelectProject changes the sidebar/new-session target without mutating the
// currently open conversation.
func (c *Controller) SelectProject(path string) error {
	canonical, err := canonicalProjectPath(path)
	if err != nil {
		return err
	}
	projectFileMu.Lock()
	defer projectFileMu.Unlock()
	state, err := loadProjectFile()
	if err != nil {
		return err
	}
	found := false
	now := time.Now().UTC()
	for i := range state.Projects {
		if state.Projects[i].Path == canonical {
			state.Projects[i].LastOpened = now
			found = true
			break
		}
	}
	if !found {
		return errors.New("project folder is not in the sidebar")
	}
	state.Selected = canonical
	return writeProjectFile(state)
}

// RemoveProject removes one sidebar row only. Session files and project files
// are untouched.
func (c *Controller) RemoveProject(path string) error {
	canonical, err := projectRecordPath(path)
	if err != nil {
		return err
	}
	projectFileMu.Lock()
	defer projectFileMu.Unlock()
	state, err := loadProjectFile()
	if err != nil {
		return err
	}
	out := state.Projects[:0]
	found := false
	for _, project := range state.Projects {
		if project.Path == canonical {
			found = true
			continue
		}
		out = append(out, project)
	}
	if !found {
		return errors.New("project folder is not in the sidebar")
	}
	state.Projects = out
	if state.Selected == canonical {
		state.Selected = ""
		if len(out) > 0 {
			state.Selected = out[0].Path
		}
	}
	return writeProjectFile(state)
}

func ensureInitialProject(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	canonical, err := canonicalProjectPath(path)
	if err != nil {
		return err
	}
	projectFileMu.Lock()
	defer projectFileMu.Unlock()
	state, err := loadProjectFile()
	if err != nil {
		return err
	}
	for _, project := range state.Projects {
		if project.Path == canonical {
			if state.Selected == "" {
				state.Selected = canonical
				return writeProjectFile(state)
			}
			return nil
		}
	}
	now := time.Now().UTC()
	state.Projects = append(state.Projects, ProjectFolder{
		Path: canonical, Name: filepath.Base(canonical), AddedAt: now, LastOpened: now,
	})
	if state.Selected == "" {
		state.Selected = canonical
	}
	return writeProjectFile(state)
}

func canonicalProjectPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("project folder is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("project folder: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("project folder: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("project folder is not a directory")
	}
	return filepath.Clean(resolved), nil
}

func projectRecordPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("project folder is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(abs); resolveErr == nil {
		abs = resolved
	}
	return filepath.Clean(abs), nil
}

func sameProjectPath(left, right string) bool {
	leftCanonical, leftErr := canonicalProjectPath(left)
	rightCanonical, rightErr := canonicalProjectPath(right)
	if leftErr == nil && rightErr == nil {
		return leftCanonical == rightCanonical
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func loadProjectFile() (projectFile, error) {
	path := golderhome.File(golderhome.GUIProjectsFile)
	if path == "" {
		return projectFile{}, errors.New("golder state root unavailable")
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return projectFile{Version: projectFileVersion}, nil
	}
	if err != nil {
		return projectFile{}, err
	}
	if len(raw) > 2<<20 {
		return projectFile{}, errors.New("GUI project registry exceeds size limit")
	}
	var state projectFile
	if json.Unmarshal(raw, &state) != nil || state.Version != projectFileVersion {
		return projectFile{}, errors.New("GUI project registry is invalid")
	}
	return state, nil
}

func writeProjectFile(state projectFile) error {
	path := golderhome.File(golderhome.GUIProjectsFile)
	if path == "" {
		return errors.New("golder state root unavailable")
	}
	state.Version = projectFileVersion
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".gui-projects-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(raw)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}
