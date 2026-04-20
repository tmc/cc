package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tmc/cc"
)

var (
	checkFlag    = flag.Bool("check", false, "Check if cache directory exists")
	createFlag   = flag.Bool("create", false, "Create cache directory if needed")
	sessionsFlag = flag.Bool("sessions", false, "Print sessions subdirectory")
	shortFlag    = flag.Bool("short", false, "Use ~ instead of full home path")
	geminiFlag   = flag.Bool("gemini", false, "Use Gemini CLI path instead of Claude Code")
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
	encoded := cc.EncodePath(absDir)

	// Build cache path
	var ch string
	if *geminiFlag {
		ch, _ = cc.GeminiHome()
	} else {
		ch, _ = cc.ClaudeHome()
	}
	cachePath := filepath.Join(ch, "projects", encoded)
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

