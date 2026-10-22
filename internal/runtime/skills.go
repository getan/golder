// This file implements declarative skills (US-028, #45): a skill is a markdown
// file with a YAML frontmatter block (mirrors SKILL.md) that names a reusable
// capability. Loading a skill parses its metadata (name, description, optional
// tool allow-list and model) and its markdown body (the skill's system prompt),
// then materializes it as a sub-agent tool — so invoking a skill runs its body
// as the system prompt of a child agent loop, reusing the SubAgentTool
// abstraction rather than introducing a second execution path. Each skill's
// description is surfaced in the parent's capability list so the model can
// choose to delegate to it.
package runtime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/getan/golder/internal/agentcore"
	"gopkg.in/yaml.v3"
)

// SkillFrontmatter is the YAML metadata block at the head of a skill file.
type SkillFrontmatter struct {
	// Name is the skill identifier; it becomes the spawnable tool name. When
	// omitted it defaults to the file's base name (without extension).
	Name string `yaml:"name"`
	// Description tells the model what the skill does and when to use it. It is
	// injected into the capability list, so it should be action-oriented.
	Description string `yaml:"description"`
	// AllowedTools optionally restricts the tools the skill's sub-agent may use,
	// by tool name. Empty means "inherit the provided tool set as-is". Real
	// Claude Code skills write this either as a YAML list or as a single
	// scalar string (e.g. "Bash(foo:*), Read"), so it tolerates both forms.
	AllowedTools stringList `yaml:"allowed-tools"`
	// Model optionally pins the skill to a specific model; empty inherits.
	Model string `yaml:"model"`
	// DisableModelInvocation, when true, keeps the skill out of the system
	// prompt's <available_skills> list so the model cannot auto-invoke it; it
	// remains reachable only via its explicit "/name" slash command (mirrors pi's
	// disable-model-invocation frontmatter key). Defaults to false.
	DisableModelInvocation bool `yaml:"disable-model-invocation"`
}

// Agent Skills spec limits (mirrors pi/agentskills.io): a skill name is a short
// slug and a description is a single sentence, both bounded so they stay cheap
// to inject into the system prompt. A description that overruns the limit is
// shortened rather than rejected: see truncateSkillDescription.
const (
	maxSkillNameLength        = 64
	maxSkillDescriptionLength = 1024
)

// skillNamePattern matches a valid skill name per the Agent Skills spec:
// lowercase ASCII letters, digits, and hyphens only.
var skillNamePattern = regexp.MustCompile(`^[a-z0-9-]+$`)

// validateSkillName reports why name violates the Agent Skills spec, or nil
// when it is valid: lowercase a-z/0-9/hyphen only, at most maxSkillNameLength
// characters, and no leading, trailing, or consecutive hyphens.
func validateSkillName(name string) error {
	if len(name) > maxSkillNameLength {
		return fmt.Errorf("name exceeds %d characters (%d)", maxSkillNameLength, len(name))
	}
	if !skillNamePattern.MatchString(name) {
		return fmt.Errorf("name %q contains invalid characters (allowed: lowercase a-z, 0-9, hyphen)", name)
	}
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") {
		return fmt.Errorf("name %q must not start or end with a hyphen", name)
	}
	if strings.Contains(name, "--") {
		return fmt.Errorf("name %q must not contain consecutive hyphens", name)
	}
	return nil
}

// validateSkillDescription reports why description cannot be used, or nil when
// it is usable: non-empty. An over-long description is not an error — dropping
// the skill would also drop every command it exposes — so truncateSkillDescription
// shortens it instead.
func validateSkillDescription(description string) error {
	if strings.TrimSpace(description) == "" {
		return errors.New("frontmatter missing required 'description'")
	}
	return nil
}

// truncateSkillDescription shortens description to maxSkillDescriptionLength
// characters, appending an ellipsis to mark the cut. It returns the description
// unchanged when it already fits, and a warning note when it does not.
//
// The limit counts runes, not bytes: the spec says "characters", and CJK text is
// three bytes per character, so a byte check would reject a compliant
// 1024-character description at roughly a third of its allowance. Cutting on a
// rune boundary keeps the result valid UTF-8.
func truncateSkillDescription(description string) (string, string) {
	total := utf8.RuneCountInString(description)
	if total <= maxSkillDescriptionLength {
		return description, ""
	}
	// Reserve the final character for the ellipsis so the result itself stays
	// within the limit.
	runes := []rune(description)
	truncated := strings.TrimRightFunc(string(runes[:maxSkillDescriptionLength-1]), unicode.IsSpace)
	if truncated == "" {
		truncated = string(runes[:maxSkillDescriptionLength-1])
	}
	truncated += "…"
	note := fmt.Sprintf("description truncated from %d to %d characters (limit %d)",
		total, utf8.RuneCountInString(truncated), maxSkillDescriptionLength)
	return truncated, note
}

// stringList is a []string that unmarshals from either a YAML sequence
// (- a\n- b) or a single scalar. A scalar is split on commas so the common
// Claude Code form `allowed-tools: Bash(foo:*), Read` parses into two entries.
// This tolerance matters: a strict []string field rejects the scalar form and,
// because a rejected file is a skill the user silently loses, one unusual
// spelling must not decide whether the skill exists at all.
type stringList []string

// UnmarshalYAML accepts a scalar or a sequence node.
func (l *stringList) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		var s string
		if err := node.Decode(&s); err != nil {
			return err
		}
		parts := strings.Split(s, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if t := strings.TrimSpace(p); t != "" {
				out = append(out, t)
			}
		}
		*l = out
		return nil
	case yaml.SequenceNode:
		var ss []string
		if err := node.Decode(&ss); err != nil {
			return err
		}
		*l = ss
		return nil
	default:
		// An empty/null node leaves the list nil (no restriction).
		return nil
	}
}

// Skill is a parsed skill file: its metadata plus the markdown body that serves
// as the sub-agent's system prompt.
type Skill struct {
	Frontmatter SkillFrontmatter
	// Body is the markdown after the frontmatter block — the skill's instructions,
	// used as the child agent's system prompt.
	Body string
	// Path is the source file, retained for diagnostics.
	Path string
	// Warnings records non-fatal adjustments made while parsing (currently a
	// description shortened to the spec limit), so LoadSkillsDir can report them
	// without skipping the skill.
	Warnings []string
}

// ParseSkill parses a skill's raw file content into a Skill. The file must open
// with a YAML frontmatter block delimited by lines containing only "---"; the
// remainder is the markdown body. A missing or malformed frontmatter block is
// an error, since name/description drive discovery.
func ParseSkill(path string, content []byte) (*Skill, error) {
	fm, body, err := splitFrontmatter(content)
	if err != nil {
		return nil, fmt.Errorf("skill %s: %w", path, err)
	}
	var meta SkillFrontmatter
	if err := yaml.Unmarshal(fm, &meta); err != nil {
		return nil, fmt.Errorf("skill %s: parse frontmatter: %w", path, err)
	}
	if meta.Name == "" {
		base := filepath.Base(path)
		meta.Name = strings.TrimSuffix(base, filepath.Ext(base))
	}
	if err := validateSkillName(meta.Name); err != nil {
		return nil, fmt.Errorf("skill %s: %w", path, err)
	}
	if err := validateSkillDescription(meta.Description); err != nil {
		return nil, fmt.Errorf("skill %s: %w", path, err)
	}
	description, warning := truncateSkillDescription(meta.Description)
	meta.Description = description
	skill := &Skill{Frontmatter: meta, Body: strings.TrimSpace(string(body)), Path: path}
	if warning != "" {
		skill.Warnings = append(skill.Warnings, warning)
	}
	return skill, nil
}

// splitFrontmatter separates a leading "---"-delimited YAML block from the rest
// of the document. It returns the frontmatter bytes (without the fences) and
// the remaining body. It errors if the document does not open with a fence or
// the closing fence is missing.
func splitFrontmatter(content []byte) (frontmatter, body []byte, err error) {
	text := string(content)
	// Tolerate a UTF-8 BOM and leading blank lines before the opening fence.
	text = strings.TrimPrefix(text, "\ufeff")
	trimmed := strings.TrimLeft(text, "\r\n")
	if !strings.HasPrefix(trimmed, "---") {
		return nil, nil, fmt.Errorf("missing YAML frontmatter (file must start with '---')")
	}
	lines := strings.Split(trimmed, "\n")
	// lines[0] is the opening fence. Find the closing fence.
	var fmLines []string
	closeIdx := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], "\r") == "---" {
			closeIdx = i
			break
		}
		fmLines = append(fmLines, lines[i])
	}
	if closeIdx == -1 {
		return nil, nil, fmt.Errorf("unterminated YAML frontmatter (missing closing '---')")
	}
	bodyLines := lines[closeIdx+1:]
	return []byte(strings.Join(fmLines, "\n")), []byte(strings.Join(bodyLines, "\n")), nil
}

// LoadSkillsDir loads every "*.md" skill file in dir (non-recursively) plus any
// "<name>/SKILL.md" nested layout (mirrors the SKILL.md convention). It returns the
// parsed skills sorted by name. A missing directory yields no skills and no
// error (skills are optional).
//
// A malformed skill file does NOT abort the load: the file is skipped and its
// error accumulated, so one bad skill cannot hide every other skill in the
// directory (a real ~/.agents/skills holds 100+ skills authored to varying
// conventions). The successfully parsed skills are always returned; the error,
// when non-nil, joins every skip reason and non-fatal warning (a truncated
// description, say) for the caller to surface.
func LoadSkillsDir(dir string) ([]*Skill, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read skills dir %s: %w", dir, err)
	}
	var skills []*Skill
	var errs []error
	for _, e := range entries {
		var path string
		switch {
		case e.IsDir():
			// Nested layout: <dir>/<name>/SKILL.md.
			candidate := filepath.Join(dir, e.Name(), "SKILL.md")
			if _, statErr := os.Stat(candidate); statErr != nil {
				continue
			}
			path = candidate
		case strings.EqualFold(filepath.Ext(e.Name()), ".md"):
			path = filepath.Join(dir, e.Name())
		default:
			continue
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			errs = append(errs, fmt.Errorf("read skill %s: %w", path, readErr))
			continue
		}
		skill, parseErr := ParseSkill(path, content)
		if parseErr != nil {
			errs = append(errs, parseErr)
			continue
		}
		for _, warning := range skill.Warnings {
			errs = append(errs, fmt.Errorf("skill %s: warning: %s", path, warning))
		}
		skills = append(skills, skill)
	}
	sort.Slice(skills, func(i, j int) bool {
		return skills[i].Frontmatter.Name < skills[j].Frontmatter.Name
	})
	return skills, errors.Join(errs...)
}

// SubAgentSpec turns a skill into a sub-agent spec: the skill body becomes the
// child's system prompt, the description is surfaced to the model, and the tool
// set is the provided tools filtered by AllowedTools (when set). newRunConfig
// builds each child run's configuration; it receives the resolved tool set so
// the caller can wire a matching registry.
func (s *Skill) SubAgentSpec(tools []agentcore.AgentTool, newRunConfig func(tools []agentcore.AgentTool) RunConfig) SubAgentSpec {
	resolved := filterToolsByName(tools, s.Frontmatter.AllowedTools)
	return SubAgentSpec{
		Name:         s.Frontmatter.Name,
		Description:  s.Frontmatter.Description,
		SystemPrompt: s.Body,
		Tools:        resolved,
		NewRunConfig: func() RunConfig { return newRunConfig(resolved) },
	}
}

// SlashCommand exposes the skill as a "/name" slash command (mirrors Claude Code's
// /skill-name invocation). Invoking it expands to the skill's instructions (its
// markdown body) as the prompt, with any arguments appended, so the skill runs
// in the current conversation. It is a prompt command (not an action): the
// expanded text is fed to the agent loop as the next user turn.
func (s *Skill) SlashCommand() SlashCommand {
	body := s.Body
	return SlashCommand{
		Name:        s.Frontmatter.Name,
		Description: s.Frontmatter.Description,
		Source:      SourceUser,
		Expand: func(args string) string {
			if strings.Contains(body, "$ARGUMENTS") {
				return strings.ReplaceAll(body, "$ARGUMENTS", args)
			}
			if strings.TrimSpace(args) == "" {
				return body
			}
			return body + "\n\n" + args
		},
	}
}

// FormatSkillsForPrompt renders the visible skills as an <available_skills>
// XML block for injection into the system prompt (mirrors pi's
// formatSkillsForPrompt). It implements progressive disclosure: only each
// skill's name, description, and location (the absolute SKILL.md path) are
// listed, so the model can read the file on demand rather than carrying every
// skill body in context.
//
// Skills with DisableModelInvocation == true are excluded (they remain
// reachable only via their explicit "/name" slash command). When no visible
// skill remains, it returns the empty string so callers can append
// unconditionally without altering a skill-free prompt.
func FormatSkillsForPrompt(skills []*Skill) string {
	visible := make([]*Skill, 0, len(skills))
	for _, s := range skills {
		if s != nil && !s.Frontmatter.DisableModelInvocation {
			visible = append(visible, s)
		}
	}
	if len(visible) == 0 {
		return ""
	}
	lines := []string{
		"\n\nThe following skills provide specialized instructions for specific tasks.",
		"Use the read tool to load a skill's file when the task matches its description.",
		"When a skill file references a relative path, resolve it against the skill directory (parent of SKILL.md / dirname of the path) and use that absolute path in tool commands.",
		"",
		"<available_skills>",
	}
	for _, s := range visible {
		lines = append(lines,
			"  <skill>",
			"    <name>"+escapeXML(s.Frontmatter.Name)+"</name>",
			"    <description>"+escapeXML(s.Frontmatter.Description)+"</description>",
			"    <location>"+escapeXML(skillLocation(s.Path))+"</location>",
			"  </skill>",
		)
	}
	lines = append(lines, "</available_skills>")
	return strings.Join(lines, "\n")
}

// skillLocation returns the absolute path to a skill file so the model can load
// it with the read tool regardless of the working directory. It falls back to
// the original path if resolution fails.
func skillLocation(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

// escapeXML escapes the five XML special characters so a skill's name or
// description cannot break the surrounding markup.
func escapeXML(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return r.Replace(s)
}

// filterToolsByName keeps only tools whose Name is in allow. An empty allow
// list means "no restriction" and returns the input unchanged.
func filterToolsByName(tools []agentcore.AgentTool, allow []string) []agentcore.AgentTool {
	if len(allow) == 0 {
		return tools
	}
	set := make(map[string]bool, len(allow))
	for _, n := range allow {
		set[n] = true
	}
	out := make([]agentcore.AgentTool, 0, len(tools))
	for _, t := range tools {
		if set[t.Name()] {
			out = append(out, t)
		}
	}
	return out
}
