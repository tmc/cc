package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
)

var version = "dev"

type subcommandSpec struct {
	binary string
	args   []string
}

// subcommands maps command names and aliases to binaries and default args.
var subcommands = map[string]subcommandSpec{
	// Full names
	"cass":     {binary: "cass"},
	"msg":      {binary: "cmsg"},
	"replay":   {binary: "creplay"},
	"sid":      {binary: "mksid"},
	"history":  {binary: "cchistory"},
	"config":   {binary: "ccconfig"},
	"loc":      {binary: "ccloc"},
	"sessions": {binary: "ccsessions"},
	"cat":      {binary: "cccat"},
	"stats":    {binary: "ccstats"},
	"tool":     {binary: "cctool"},
	"files":    {binary: "ccfiles"},
	"diff":     {binary: "ccdiff"},
	"time":     {binary: "cctime"},
	"err":      {binary: "ccerr"},
	"team":     {binary: "ccteam"},
	"spawn":    {binary: "ccspawn"},
	"inbox":    {binary: "ccinbox"},
	"task":     {binary: "cctask"},
	"agent":    {binary: "ccagent"},
	"approve":  {binary: "ccapprove"},
	"handoff":  {binary: "cchandoff"},
	"memory":   {binary: "ccmemory"},
	"goals":    {binary: "cass", args: []string{"goals"}},
	"skills":   {binary: "cass", args: []string{"skills"}},
	"requests": {binary: "cass", args: []string{"requests"}},
	"web":      {binary: "cass", args: []string{"web"}},

	// Short aliases
	"m":  {binary: "cmsg"},
	"r":  {binary: "creplay"},
	"s":  {binary: "mksid"},
	"h":  {binary: "cchistory"},
	"c":  {binary: "ccconfig"},
	"l":  {binary: "ccloc"},
	"ss": {binary: "ccsessions"},
	"t":  {binary: "ccteam"},
	"sp": {binary: "ccspawn"},
	"in": {binary: "ccinbox"},
	"tk": {binary: "cctask"},
	"ag": {binary: "ccagent"},
	"ap": {binary: "ccapprove"},
	"ho": {binary: "cchandoff"},
	"mm": {binary: "ccmemory"},
}

func resolveSubcommand(name string) (subcommandSpec, bool) {
	spec, ok := subcommands[name]
	return spec, ok
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "help", "--help", "-h":
		os.Exit(runHelp(args))
		return

	case "version", "--version", "-v":
		printVersion()
		return
	}

	// Look up subcommand
	spec, ok := resolveSubcommand(cmd)
	if !ok {
		fmt.Fprintf(os.Stderr, "cctl: unknown command %q\n", cmd)
		fmt.Fprintf(os.Stderr, "Run 'cctl help' for usage.\n")
		os.Exit(2)
	}

	os.Exit(runSubcommand(spec.binary, append(spec.args, args...)))
}

func runSubcommand(binary string, args []string) int {
	// Try to find binary in PATH first
	path, err := exec.LookPath(binary)
	if err != nil {
		// Try relative to cctl binary
		self, err := os.Executable()
		if err == nil {
			path = filepath.Join(filepath.Dir(self), binary)
			if _, err := os.Stat(path); err != nil {
				path = ""
			}
		}
	}

	if path == "" {
		fmt.Fprintf(os.Stderr, "cctl: subcommand %q not found\n", binary)
		fmt.Fprintf(os.Stderr, "Try: go install github.com/tmc/cc/cmd/%s@latest\n", binary)
		return 2
	}

	// Execute subcommand
	cmd := exec.Command(path, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "cctl: %v\n", err)
		return 1
	}

	return 0
}

func runHelp(args []string) int {
	if len(args) == 0 {
		usage()
		return 0
	}
	spec, ok := resolveSubcommand(args[0])
	if !ok {
		fmt.Fprintf(os.Stderr, "cctl: unknown command %q\n", args[0])
		fmt.Fprintf(os.Stderr, "Run 'cctl help' for usage.\n")
		return 2
	}
	return runSubcommand(spec.binary, append(spec.args, "--help"))
}

func usage() {
	fmt.Print(`cctl - Claude Code Utilities

Usage:
  cctl <command> [options] [args]

Commands:
  cass         Show the cass CLI
  msg, m       Format stdin as Claude messages
  replay, r    Replay Claude Code sessions in TUI
  sid, s       Generate session IDs
  history, h   Search session history
  config, c    Manage configuration
  loc, l       Show agent cache location
  team, t      Manage agent teams
  spawn, sp    Spawn Claude Code agents
  inbox, in    Read/write agent inbox messages
  task, tk     Manage persistent tasks
  agent, ag    Inspect agent status
  approve, ap  Handle approval workflows
  handoff, ho  Build cross-tool session handoff prompt
	memory, mm   List and read auto-memory files
  goals        Show goal-mode objectives indexed by cass
  skills       Show skill usage indexed by cass
  requests     Show indexed API request breakdown
  web          Show the cass web UI
  help         Show help for a command
  version      Show version information

Examples:
  echo "Hello" | cctl msg
  cctl sid
  cctl history -since 24h "error"
  cctl team -create review
  echo "Review PR" | cctl inbox -team review -to reviewer
  cctl handoff -from session.jsonl -to gemini

Run 'cctl help <command>' for more information on a specific command.
`)
}

func printVersion() {
	fmt.Printf("cctl version %s\n", version)
	fmt.Println("\nSubcommands:")
	names := make([]string, 0, len(subcommands))
	for name := range subcommands {
		if len(name) == 1 {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		spec := subcommands[name]
		if path, err := exec.LookPath(spec.binary); err == nil {
			fmt.Printf("  %-10s %s\n", name, path)
		} else {
			fmt.Printf("  %-10s (not installed)\n", name)
		}
	}
}
