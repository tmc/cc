package collector

import (
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
	skillHeaderRe    = regexp.MustCompile(`(?m)^## Skill:\s*([A-Za-z0-9_.:-]+)|<skill(?:\s+name=["']?([A-Za-z0-9_.:-]+)["']?)?`)
	skillNameRe      = regexp.MustCompile(`(?m)^name:\s*([A-Za-z0-9_.:-]+)\s*$`)
	skillPathRe      = regexp.MustCompile(`([~/$A-Za-z0-9_.:@+-][~/A-Za-z0-9_.:@+-]*/SKILL\.md)`)
)

// ExtractSkills returns skill signals observed in session entries.
func ExtractSkills(entries []cc.Entry, source string) []cass.SkillUse {
	m := map[string]*cass.SkillUse{}
	add := func(s cass.SkillUse, at time.Time) {
		s.Name = strings.TrimSpace(s.Name)
		s.Path = strings.TrimSpace(s.Path)
		if s.Name == "" && s.Path != "" {
			s.Name = skillNameFromPath(s.Path)
		}
		if s.Name == "" {
			return
		}
		if s.Kind == "" {
			s.Kind = "tool"
		}
		if s.Source == "" {
			s.Source = source
		}
		if s.Count == 0 {
			s.Count = 1
		}
		if !at.IsZero() {
			s.FirstSeen = at
			s.LastSeen = at
		}

		key := s.Name + "\x00" + s.Kind + "\x00" + s.Path
		cur := m[key]
		if cur == nil {
			cp := s
			m[key] = &cp
			return
		}
		cur.Count += s.Count
		if cur.Path == "" {
			cur.Path = s.Path
		}
		if cur.Source == "" {
			cur.Source = s.Source
		}
		if !s.FirstSeen.IsZero() && (cur.FirstSeen.IsZero() || s.FirstSeen.Before(cur.FirstSeen)) {
			cur.FirstSeen = s.FirstSeen
		}
		if !s.LastSeen.IsZero() && (cur.LastSeen.IsZero() || s.LastSeen.After(cur.LastSeen)) {
			cur.LastSeen = s.LastSeen
		}
		cur.Evidence = mergeEvidence(cur.Evidence, s.Evidence)
	}

	for _, e := range entries {
		if e.Message == nil {
			continue
		}
		text := e.Message.TextContent()
		for _, match := range availableSkillRe.FindAllStringSubmatch(text, -1) {
			add(cass.SkillUse{
				Name:     match[1],
				Path:     match[2],
				Kind:     "available",
				Evidence: []string{"available skills list"},
			}, e.Timestamp)
		}
		for _, match := range skillHeaderRe.FindAllStringSubmatch(text, -1) {
			name := match[1]
			if name == "" {
				name = match[2]
			}
			if name == "" {
				name = firstSkillName(text)
			}
			add(cass.SkillUse{
				Name:     name,
				Kind:     "expanded",
				Evidence: []string{"skill-expanded prompt context"},
			}, e.Timestamp)
		}

		for _, b := range e.Message.ToolUses() {
			switch b.Name {
			case "Skill":
				if name := skillNameFromInput(b.Input); name != "" {
					add(cass.SkillUse{
						Name:     name,
						Kind:     "selected",
						Evidence: []string{"Skill tool invocation"},
					}, e.Timestamp)
				}
			case "Read":
				if path := skillPathFromInput(b.Input); path != "" {
					add(cass.SkillUse{
						Name:     skillNameFromPath(path),
						Path:     path,
						Kind:     "loaded",
						Evidence: []string{"Read tool loaded SKILL.md"},
					}, e.Timestamp)
				}
			case "Bash":
				for _, path := range skillPathRe.FindAllString(b.BashCommand(), -1) {
					add(cass.SkillUse{
						Name:     skillNameFromPath(path),
						Path:     path,
						Kind:     "loaded",
						Evidence: []string{"shell command referenced SKILL.md"},
					}, e.Timestamp)
				}
			default:
				if looksLikeSkillTool(b.Name) {
					add(cass.SkillUse{
						Name:     b.Name,
						Kind:     "tool",
						Evidence: []string{"skill-like tool invocation"},
					}, e.Timestamp)
				}
			}
		}
	}

	out := make([]cass.SkillUse, 0, len(m))
	for _, s := range m {
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
		FilePath string `json:"file_path"`
		Path     string `json:"path"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return ""
	}
	path := in.FilePath
	if path == "" {
		path = in.Path
	}
	if strings.HasSuffix(path, "/SKILL.md") {
		return path
	}
	return ""
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
	return dir
}

func firstSkillName(text string) string {
	if m := skillNameRe.FindStringSubmatch(text); len(m) == 2 {
		return m[1]
	}
	if m := skillPathRe.FindString(text); m != "" {
		return skillNameFromPath(m)
	}
	return ""
}

func looksLikeSkillTool(name string) bool {
	if name == "" || cass.BuiltinTools[name] || strings.HasPrefix(name, "mcp__") {
		return false
	}
	return strings.Contains(name, "-") || strings.Contains(name, ":")
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
