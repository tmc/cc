package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

var version = "dev"

// subcommands maps command names and aliases to binary names.
var subcommands = map[string]string{
	// Full names
	"msg":      "cmsg",
	"replay":   "creplay",
	"sid":      "mksid",
	"history":  "cchistory",
	"config":   "ccconfig",
	"loc":      "ccloc",
	"sessions": "ccsessions",
	"cat":      "cccat",
	"stats":    "ccstats",
	"tool":     "cctool",
	"files":    "ccfiles",
	"diff":     "ccdiff",
	"time":     "cctime",
	"err":      "ccerr",
	"team":     "ccteam",
	"spawn":    "ccspawn",
	"inbox":    "ccinbox",
	"task":     "cctask",
	"agent":    "ccagent",
	"approve":  "ccapprove",
	"memory":   "ccmemory",

	// Short aliases
	"m":  "cmsg",
	"r":  "creplay",
	"s":  "mksid",
	"h":  "cchistory",
	"c":  "ccconfig",
	"l":  "ccloc",
	"ss": "ccsessions",
	"t":  "ccteam",
	"sp": "ccspawn",
	"in": "ccinbox",
	"tk": "cctask",
	"ag": "ccagent",
	"ap": "ccapprove",
	"mm": "ccmemory",
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
		if len(args) > 0 {
			// Show help for subcommand
			runSubcommand(args[0], []string{"--help"})
		} else {
			usage()
		}
		return

	case "version", "--version", "-v":
		printVersion()
		return
	}

	// Look up subcommand
	binary, ok := subcommands[cmd]
	if !ok {
		fmt.Fprintf(os.Stderr, "cctl: unknown command %q\n", cmd)
		fmt.Fprintf(os.Stderr, "Run 'cctl help' for usage.\n")
		os.Exit(2)
	}

	os.Exit(runSubcommand(binary, args))
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

func usage() {
	fmt.Print(`cctl - Claude Code Utilities

Usage:
  cctl <command> [options] [args]

Commands:
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
  memory, mm   List and read auto-memory files
  help         Show help for a command
  version      Show version information

Examples:
  echo "Hello" | cctl msg
  cctl sid
  cctl history -since 24h "error"
  cctl team -create review
  echo "Review PR" | cctl inbox -team review -to reviewer

Run 'cctl help <command>' for more information on a specific command.
`)
}

func printVersion() {
	fmt.Printf("cctl version %s\n", version)
	fmt.Println("\nSubcommands:")
	for name, binary := range subcommands {
		// Skip aliases
		if len(name) == 1 {
			continue
		}
		if path, err := exec.LookPath(binary); err == nil {
			fmt.Printf("  %-10s %s\n", name, path)
		} else {
			fmt.Printf("  %-10s (not installed)\n", name)
		}
	}
}
