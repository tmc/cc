package ccmodel

import "testing"

func TestIsHarnessPreamble(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{name: "instruction file", text: "# AGENTS.md instructions\nUse tabs.", want: true},
		{name: "other instruction file", text: "# Project.md Instructions\nUse tabs.", want: true},
		{name: "environment", text: "<environment_context>cwd=/work</environment_context>", want: true},
		{name: "permissions", text: "<permissions instructions>network denied</permissions instructions>", want: true},
		{name: "user prompt", text: "update the session title", want: false},
		{name: "mentioned header", text: "Explain # AGENTS.md instructions.", want: false},
		{name: "request mentioning instruction file", text: "# Please read AGENTS.md instructions", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsHarnessPreamble(tt.text); got != tt.want {
				t.Errorf("IsHarnessPreamble(%q) = %t, want %t", tt.text, got, tt.want)
			}
		})
	}
}
