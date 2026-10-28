package extensions

import (
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/getan/golder/internal/runtime"
)

type SkillView struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
	State       string `json:"state"`
	ProvidedBy  string `json:"provided_by,omitempty"`
}
type skillSet struct {
	mu          sync.RWMutex
	independent []*runtime.Skill
	disabled    map[string]bool
	off         bool
}

func (m *Manager) SetSkills(skills []*runtime.Skill, off bool) {
	m.skills.mu.Lock()
	defer m.skills.mu.Unlock()
	m.skills.independent = append([]*runtime.Skill{}, skills...)
	m.skills.off = off
	if m.skills.disabled == nil {
		m.skills.disabled = map[string]bool{}
	}
	m.revision.Add(1)
}

func (m *Manager) allSkills() ([]*runtime.Skill, map[string]string) {
	m.skills.mu.RLock()
	skills := append([]*runtime.Skill{}, m.skills.independent...)
	off := m.skills.off
	m.skills.mu.RUnlock()
	owners := map[string]string{}
	if off {
		return nil, owners
	}
	seen := map[string]bool{}
	for _, s := range skills {
		seen[s.Frontmatter.Name] = true
	}
	dirs := m.PackageSkills()
	keys := make([]string, 0, len(dirs))
	for k := range dirs {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, owner := range keys {
		ss, _ := runtime.LoadSkillsDir(dirs[owner])
		for _, s := range ss {
			if !seen[s.Frontmatter.Name] {
				seen[s.Frontmatter.Name] = true
				skills = append(skills, s)
				owners[s.Path] = owner
			}
		}
	}
	return skills, owners
}
func (m *Manager) Skills() []*runtime.Skill {
	all, _ := m.allSkills()
	m.skills.mu.RLock()
	defer m.skills.mu.RUnlock()
	var out []*runtime.Skill
	for _, s := range all {
		if !m.skills.disabled[s.Frontmatter.Name] {
			out = append(out, s)
		}
	}
	return out
}
func (m *Manager) SkillViews() []SkillView {
	all, owners := m.allSkills()
	m.skills.mu.RLock()
	defer m.skills.mu.RUnlock()
	out := make([]SkillView, 0, len(all))
	for _, s := range all {
		state := "available"
		if s.Frontmatter.DisableModelInvocation {
			state = "manual only"
		}
		if m.skills.disabled[s.Frontmatter.Name] {
			state = "disabled"
		}
		out = append(out, SkillView{Name: s.Frontmatter.Name, Description: clean(s.Frontmatter.Description, 2048), Path: s.Path, State: state, ProvidedBy: owners[s.Path]})
	}
	slices.SortFunc(out, func(a, b SkillView) int { return strings.Compare(a.Name, b.Name) })
	return out
}
func (m *Manager) SetSkillEnabled(name string, enabled bool) bool {
	all, _ := m.allSkills()
	for _, s := range all {
		if s.Frontmatter.Name == name {
			m.skills.mu.Lock()
			m.skills.disabled[name] = !enabled
			m.skills.mu.Unlock()
			m.revision.Add(1)
			return true
		}
	}
	return false
}
func (m *Manager) Skill(name string) *runtime.Skill {
	for _, s := range m.Skills() {
		if s.Frontmatter.Name == name {
			return s
		}
	}
	return nil
}
func (m *Manager) SkillRoots() []string {
	var out []string
	for _, s := range m.Skills() {
		out = append(out, filepath.Dir(s.Path))
	}
	return out
}

func (m *Manager) Revision() uint64 { return m.revision.Load() }
