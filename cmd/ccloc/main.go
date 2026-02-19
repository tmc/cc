package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var (
	checkFlag    = flag.Bool("check", false, "Check if cache directory exists")
	createFlag   = flag.Bool("create", false, "Create cache directory if needed")
	sessionsFlag = flag.Bool("sessions", false, "Print sessions subdirectory")
	shortFlag    = flag.Bool("short", false, "Use ~ instead of full home path")
)

func main() {
	flag.Parse()

	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "ccloc: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// Get target directory
	dir := "."
	if flag.NArg() > 0 {
		dir = flag.Arg(0)
	}

	// Resolve to absolute path
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}

	// Get home directory
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("get home directory: %w", err)
	}

	// Encode the path
	encoded := encodePath(absDir)

	// Build cache path
	cachePath := filepath.Join(home, ".claude", "projects", encoded)
	if *sessionsFlag {
		cachePath = filepath.Join(cachePath, "sessions")
	}

	// Check if exists
	if *checkFlag {
		if _, err := os.Stat(cachePath); err != nil {
			return fmt.Errorf("cache directory does not exist")
		}
		return nil
	}

	// Create if requested
	if *createFlag {
		if err := os.MkdirAll(cachePath, 0755); err != nil {
			return fmt.Errorf("create directory: %w", err)
		}
	}

	// Output path
	output := cachePath
	if *shortFlag {
		output = strings.Replace(cachePath, home, "~", 1)
	}

	fmt.Println(output)
	return nil
}

// encodePath converts a filesystem path to Claude Code's cache directory format.
// Example: /Volumes/tmc/go/src/github.com/tmc/cc → -Volumes-tmc-go-src-github-com-tmc-cc
func encodePath(path string) string {
	// Replace path separators with dashes
	encoded := strings.ReplaceAll(path, string(filepath.Separator), "-")
	// Replace dots with dashes (for domain-like paths)
	encoded = strings.ReplaceAll(encoded, ".", "-")
	return encoded
}
