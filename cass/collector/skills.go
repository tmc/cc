package collector

import (
	"cmp"
	"encoding/json"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/tmc/cc"
	"github.com/tmc/cc/cass"
)

var (
	availableSkillRe = regexp.MustCompile(`(?m)^- ([A-Za-z0-9_.-]+(?::[A-Za-z0-9_.-]+)?): .*\(file: ([^)]+/SKILL\.md)\)`)
	skillHeaderRe    = regexp.MustCompile(`(?m)^## Skill:\s*([A-Za-z0-9_.:-]+)`)
	skillPathRe      = regexp.MustCompile(`([~/$A-Za-z0-9_.:@+-][~/A-Za-z0-9_.:@+-]*/SKILL\.md)`)

	// Claude Code injects a skill's body after a Skill tool call or a slash
	// command, starting with its directory.
	claudeSkillDirRe = regexp.MustCompile(`\ABase directory for this skill:\s*(\S+)`)
	commandNameRe    = regexp.MustCompile(`<command-name>/?([A-Za-z0-9_.:-]+)</command-name>`)
	// Codex attaches a skill the user named with $name as its own user turn.
	codexSkillRe = regexp.MustCompile(`\A\s*<skill>\s*<name>([^<]+)</name>\s*<path>([^<]+)</path>`)
	// Pi expands /skill:name into a tagged block in the user turn.
	piSkillRe = regexp.MustCompile(`\A\s*<skill name="([^"]+)"(?:\s+location="([^"]+)")?`)
)

// ExtractSkills returns skill signals observed in session entries.
func ExtractSkills(entries []cc.Entry, source string) []cass.SkillUse {
	var set skillSet
	command := ""
	for _, e := range entries {
		if e.Message == nil {
			continue
		}
		text := e.Message.TextContent()
		if e.Message.Role == "user" {
			if m := claudeSkillDirRe.FindStringSubmatch(text); m != nil {
				dir := strings.TrimRight(m[1], "/")
				path := dir + "/SKILL.md"
				if command != "" {
					set.add(cass.SkillUse{
						Name:     command,
						Path:     path,
						Kind:     "selected",
						Evidence: []string{"slash command /" + command},
					}, e.Timestamp)
				}
				set.add(cass.SkillUse{
					Name:     skillNameFromPath(path),
					Path:     path,
					Kind:     "loaded",
					Evidence: []string{"skill body injected"},
				}, e.Timestamp)
				command = ""
				continue
			}
			if m := codexSkillRe.FindStringSubmatch(text); m != nil {
				set.add(cass.SkillUse{
					Name:     strings.TrimSpace(m[1]),
					Path:     strings.TrimSpace(m[2]),
					Kind:     "selected",
					Evidence: []string{"skill attached to user turn"},
				}, e.Timestamp)
				continue
			}
			if m := piSkillRe.FindStringSubmatch(text); m != nil {
				set.add(cass.SkillUse{
					Name:     m[1],
					Path:     m[2],
					Kind:     "selected",
					Evidence: []string{"skill command expanded"},
				}, e.Timestamp)
				continue
			}
			command = ""
			if m := commandNameRe.FindStringSubmatch(text); m != nil {
				command = m[1]
			}
		}
		for _, match := range availableSkillRe.FindAllStringSubmatch(text, -1) {
			set.add(cass.SkillUse{
				Name:     match[1],
				Path:     match[2],
				Kind:     "available",
				Evidence: []string{"available skills list"},
			}, e.Timestamp)
		}
		for _, match := range skillHeaderRe.FindAllStringSubmatch(text, -1) {
			set.add(cass.SkillUse{
				Name:     match[1],
				Kind:     "expanded",
				Evidence: []string{"skill-expanded prompt context"},
			}, e.Timestamp)
		}
		for _, b := range e.Message.ToolUses() {
			set.addToolUse(b.Name, b.Input, e.Timestamp)
		}
	}
	return set.list(source)
}

// skillSet accumulates skill signals, merging repeats of the same name,
// kind and path.
type skillSet struct {
	m map[string]*cass.SkillUse
}

// addToolUse records the skill signal, if any, carried by one tool call.
// Tool names are compared without case: harnesses spell the same tool
// Read, read and read_file.
func (set *skillSet) addToolUse(name string, input json.RawMessage, at time.Time) {
	switch strings.ToLower(name) {
	case "skill", "activate_skill":
		if skill := skillNameFromInput(input); skill != "" {
			set.add(cass.SkillUse{
				Name:     skill,
				Kind:     "selected",
				Evidence: []string{name + " tool invocation"},
			}, at)
		}
	case "read", "read_file", "view":
		if path := skillPathFromInput(input); path != "" {
			set.add(cass.SkillUse{
				Name:     skillNameFromPath(path),
				Path:     path,
				Kind:     "loaded",
				Evidence: []string{"read tool loaded SKILL.md"},
			}, at)
		}
	case "bash", "shell", "run_shell_command":
		for _, path := range skillPathRe.FindAllString(commandFromInput(input), -1) {
			set.add(cass.SkillUse{
				Name:     skillNameFromPath(path),
				Path:     path,
				Kind:     "loaded",
				Evidence: []string{"shell command referenced SKILL.md"},
			}, at)
		}
	}
}

func (set *skillSet) add(s cass.SkillUse, at time.Time) {
	s.Name = strings.TrimSpace(s.Name)
	s.Path = strings.TrimSpace(s.Path)
	if s.Name == "" && s.Path != "" {
		s.Name = skillNameFromPath(s.Path)
	}
	if s.Name == "" {
		return
	}
	if s.Count == 0 {
		s.Count = 1
	}
	if !at.IsZero() {
		s.FirstSeen = at
		s.LastSeen = at
	}

	key := s.Name + "\x00" + s.Kind + "\x00" + s.Path
	if set.m == nil {
		set.m = map[string]*cass.SkillUse{}
	}
	cur := set.m[key]
	if cur == nil {
		cp := s
		set.m[key] = &cp
		return
	}
	cur.Count += s.Count
	if !s.FirstSeen.IsZero() && (cur.FirstSeen.IsZero() || s.FirstSeen.Before(cur.FirstSeen)) {
		cur.FirstSeen = s.FirstSeen
	}
	if !s.LastSeen.IsZero() && (cur.LastSeen.IsZero() || s.LastSeen.After(cur.LastSeen)) {
		cur.LastSeen = s.LastSeen
	}
	cur.Evidence = mergeEvidence(cur.Evidence, s.Evidence)
}

// list returns the accumulated signals, oldest first, tagged with source.
func (set *skillSet) list(source string) []cass.SkillUse {
	out := make([]cass.SkillUse, 0, len(set.m))
	for _, s := range set.m {
		if s.Source == "" {
			s.Source = source
		}
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastSeen.Equal(out[j].LastSeen) {
			if out[i].Name == out[j].Name {
				return out[i].Kind < out[j].Kind
			}
			return out[i].Name < out[j].Name
		}
		return out[i].LastSeen.Before(out[j].LastSeen)
	})
	return out
}

func skillNameFromInput(raw json.RawMessage) string {
	var in struct {
		Skill string `json:"skill"`
		Name  string `json:"name"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return ""
	}
	if in.Skill != "" {
		return in.Skill
	}
	return in.Name
}

func skillPathFromInput(raw json.RawMessage) string {
	var in struct {
		FilePath     string `json:"file_path"`
		FilePathCC   string `json:"filePath"`
		AbsolutePath string `json:"absolute_path"`
		Path         string `json:"path"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return ""
	}
	path := cmp.Or(in.FilePath, in.FilePathCC, in.AbsolutePath, in.Path)
	if strings.HasSuffix(path, "/SKILL.md") {
		return path
	}
	return ""
}

func commandFromInput(raw json.RawMessage) string {
	var in struct {
		Command json.RawMessage `json:"command"`
		Cmd     string          `json:"cmd"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return ""
	}
	var s string
	if json.Unmarshal(in.Command, &s) == nil && s != "" {
		return s
	}
	var argv []string
	if json.Unmarshal(in.Command, &argv) == nil && len(argv) > 0 {
		return strings.Join(argv, " ")
	}
	return in.Cmd
}

func skillNameFromPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	dir := filepath.Base(filepath.Dir(path))
	if dir == ".system" || dir == "skills" || dir == "." || dir == string(filepath.Separator) {
		return ""
	}
	// A shell command can name SKILL.md through a variable or glob
	// ("cat $d/SKILL.md"), which names no particular skill.
	if strings.ContainsAny(dir, "$*?[{") {
		return ""
	}
	return dir
}

func mergeEvidence(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	var out []string
	for _, s := range append(a, b...) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
		if len(out) == 5 {
			break
		}
	}
	return out
}
