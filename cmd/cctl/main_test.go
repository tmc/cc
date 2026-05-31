package main

import "testing"

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
