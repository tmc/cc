package main

import (
	"slices"
	"testing"
)

func TestPagerCommand(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want []string
	}{
		{"unset falls back to less", nil, []string{"less"}},
		{"PAGER", map[string]string{"PAGER": "more"}, []string{"sh", "-c", "more"}},
		{"CCRENDER_PAGER wins", map[string]string{"CCRENDER_PAGER": "bat", "PAGER": "more"}, []string{"sh", "-c", "bat"}},
		{"PAGER with flags", map[string]string{"PAGER": "less -R"}, []string{"sh", "-c", "less -R"}},
		{"empty disables", map[string]string{"PAGER": ""}, nil},
		{"blank disables", map[string]string{"CCRENDER_PAGER": "  "}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			look := func(k string) (string, bool) {
				v, ok := tt.env[k]
				return v, ok
			}
			if got := pagerCommand(look); !slices.Equal(got, tt.want) {
				t.Errorf("pagerCommand() = %q, want %q", got, tt.want)
			}
		})
	}
}
