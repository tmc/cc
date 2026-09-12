package sessionview

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

func TestLessEnv(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		pattern string
		want    string
		wantOK  bool
	}{
		{name: "default", want: "FRX", wantOK: true},
		{name: "default with search", pattern: "panic", want: "FRX +/panic", wantOK: true},
		{name: "user LESS kept", env: map[string]string{"LESS": "R"}},
		{name: "user LESS composes", env: map[string]string{"LESS": "R"}, pattern: "panic", want: "R +/panic", wantOK: true},
		{name: "alternation", pattern: "retry|policy", want: "FRX +/retry|policy", wantOK: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			look := func(k string) (string, bool) {
				v, ok := tt.env[k]
				return v, ok
			}
			got, ok := lessEnv(look, tt.pattern)
			if ok != tt.wantOK || got != tt.want {
				t.Errorf("lessEnv = %q, %v; want %q, %v", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
