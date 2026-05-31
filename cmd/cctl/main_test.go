package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSubcommandsIncludeCassAndRequests(t *testing.T) {
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
