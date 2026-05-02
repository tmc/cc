package cc

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestTeamConfigRoundTrip(t *testing.T) {
	t.Setenv("CC_TEAMS_DIR", t.TempDir())

	want := &TeamConfig{
		Name:          "review",
		Description:   "PR review squad",
		CreatedAt:     1700000000,
		LeadAgentID:   "lead@review",
		LeadSessionID: "sess-1",
		Members: []TeamMember{
			{AgentID: "a@review", Name: "alice", AgentType: "reviewer", JoinedAt: 1700000001, CWD: "/tmp/x"},
			{AgentID: "b@review", Name: "bob", AgentType: "reviewer", JoinedAt: 1700000002, CWD: "/tmp/y"},
		},
	}
	if err := WriteTeamConfig("review", want); err != nil {
		t.Fatalf("WriteTeamConfig: %v", err)
	}
	got, err := ReadTeamConfig("review")
	if err != nil {
		t.Fatalf("ReadTeamConfig: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip mismatch:\n got = %+v\nwant = %+v", got, want)
	}
}

func TestReadTeamConfigMissing(t *testing.T) {
	t.Setenv("CC_TEAMS_DIR", t.TempDir())
	if _, err := ReadTeamConfig("does-not-exist"); err == nil {
		t.Errorf("ReadTeamConfig(missing) returned nil error")
	}
}

func TestListTeams(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CC_TEAMS_DIR", dir)

	// Empty teams dir.
	names, err := ListTeams()
	if err != nil {
		t.Fatalf("ListTeams (empty): %v", err)
	}
	if len(names) != 0 {
		t.Errorf("ListTeams (empty) = %v, want []", names)
	}

	// Create three teams; one without config.json must be ignored.
	for _, name := range []string{"review", "build", "stray"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"review", "build"} {
		if err := WriteTeamConfig(name, &TeamConfig{Name: name}); err != nil {
			t.Fatalf("WriteTeamConfig: %v", err)
		}
	}
	names, err = ListTeams()
	if err != nil {
		t.Fatalf("ListTeams: %v", err)
	}
	want := []string{"build", "review"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("ListTeams = %v, want %v", names, want)
	}
}

func TestListTeamsMissingDir(t *testing.T) {
	t.Setenv("CC_TEAMS_DIR", filepath.Join(t.TempDir(), "missing"))
	names, err := ListTeams()
	if err != nil {
		t.Fatalf("ListTeams (missing dir): %v", err)
	}
	if names != nil {
		t.Errorf("ListTeams (missing dir) = %v, want nil", names)
	}
}
