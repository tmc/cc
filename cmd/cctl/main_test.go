package main

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func TestSubcommandsIncludeCassAndCassAliases(t *testing.T) {
	cassSpec, ok := resolveSubcommand("cass")
	if !ok {
		t.Fatal("missing cass subcommand")
	}
	if cassSpec.binary != "cass" || len(cassSpec.args) != 0 {
		t.Fatalf("cass spec = %#v, want binary cass with no default args", cassSpec)
	}

	reqSpec, ok := resolveSubcommand("requests")
	if !ok {
		t.Fatal("missing requests subcommand")
	}
	if reqSpec.binary != "cass" {
		t.Fatalf("requests binary = %q, want cass", reqSpec.binary)
	}
	if len(reqSpec.args) != 1 || reqSpec.args[0] != "requests" {
		t.Fatalf("requests args = %#v, want [requests]", reqSpec.args)
	}

	for _, tc := range []struct {
		name string
		want string
	}{
		{name: "goals", want: "goals"},
		{name: "skills", want: "skills"},
		{name: "web", want: "web"},
	} {
		spec, ok := resolveSubcommand(tc.name)
		if !ok {
			t.Fatalf("missing %s subcommand", tc.name)
		}
		if spec.binary != "cass" {
			t.Fatalf("%s binary = %q, want cass", tc.name, spec.binary)
		}
		if len(spec.args) != 1 || spec.args[0] != tc.want {
			t.Fatalf("%s args = %#v, want [%s]", tc.name, spec.args, tc.want)
		}
	}
}

func TestResolveSubcommandRejectsUnknown(t *testing.T) {
	if _, ok := resolveSubcommand("unknown"); ok {
		t.Fatal("resolveSubcommand accepted unknown command")
	}
}

func TestRunSubcommandDispatchesCassRequests(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper script is Unix-specific")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "cass")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$TMPDIR/cctl-args.txt\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+oldPath)
	t.Setenv("TMPDIR", dir)

	if code := runSubcommand("cass", []string{"requests", "--help"}); code != 0 {
		t.Fatalf("runSubcommand exit code = %d, want 0", code)
	}

	got, err := os.ReadFile(filepath.Join(dir, "cctl-args.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if text := strings.TrimSpace(string(got)); text != "requests\n--help" && text != "requests --help" {
		t.Fatalf("executed args = %q, want requests and --help", text)
	}
}

func TestRunHelpDispatchesCassRequests(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper script is Unix-specific")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "cass")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$TMPDIR/cctl-help-args.txt\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+oldPath)
	t.Setenv("TMPDIR", dir)

	if code := runHelp([]string{"requests"}); code != 0 {
		t.Fatalf("runHelp exit code = %d, want 0", code)
	}

	got, err := os.ReadFile(filepath.Join(dir, "cctl-help-args.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if text := strings.TrimSpace(string(got)); text != "requests\n--help" && text != "requests --help" {
		t.Fatalf("help dispatch args = %q, want requests and --help", text)
	}
}

func TestRunHelpDispatchesCassWeb(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper script is Unix-specific")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "cass")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$TMPDIR/cctl-help-web-args.txt\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+oldPath)
	t.Setenv("TMPDIR", dir)

	if code := runHelp([]string{"web"}); code != 0 {
		t.Fatalf("runHelp exit code = %d, want 0", code)
	}

	got, err := os.ReadFile(filepath.Join(dir, "cctl-help-web-args.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if text := strings.TrimSpace(string(got)); text != "web\n--help" && text != "web --help" {
		t.Fatalf("help dispatch args = %q, want web and --help", text)
	}
}

func TestPrintVersionListsSubcommandsSorted(t *testing.T) {
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", t.TempDir()+string(os.PathListSeparator)+oldPath)

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	printVersion()
	w.Close()
	os.Stdout = oldStdout

	gotBytes, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, line := range strings.Split(string(gotBytes), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "cctl version") || line == "Subcommands:" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) > 0 {
			got = append(got, fields[0])
		}
	}
	want := append([]string(nil), got...)
	sort.Strings(want)
	if !slicesEqual(got, want) {
		t.Fatalf("version subcommands not sorted: got %v want %v", got, want)
	}
	if len(got) == 0 {
		t.Fatal("version output did not list any subcommands")
	}
}

func TestUsageListsRequests(t *testing.T) {
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	usage()
	w.Close()
	os.Stdout = oldStdout

	got, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "requests     Show indexed API request breakdown") {
		t.Fatalf("usage output missing requests entry:\n%s", got)
	}
	for _, want := range []string{
		"goals        Show goal-mode objectives indexed by cass",
		"skills       Show skill usage indexed by cass",
		"web          Show the cass web UI",
	} {
		if !strings.Contains(string(got), want) {
			t.Fatalf("usage output missing %q:\n%s", want, got)
		}
	}
}

func slicesEqual[T comparable](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
