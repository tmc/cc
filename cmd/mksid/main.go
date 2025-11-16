package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

var (
	format  = flag.String("format", "default", "Output format (default, uuid-only, timestamp-only, json)")
	noGit   = flag.Bool("no-git", false, "Disable git repository detection")
	verbose = flag.Bool("verbose", false, "Print verbose information to stderr")
)

func main() {
	flag.Parse()

	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "mksid: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// Generate timestamp
	timestamp := generateTimestamp()

	// Generate UUID
	uuid, err := generateUUID()
	if err != nil {
		return fmt.Errorf("generate uuid: %w", err)
	}

	// Generate git hash
	gitHash := generateGitHash()

	// Output based on format
	switch *format {
	case "default":
		fmt.Printf("%s-%s-%s\n", timestamp, uuid, gitHash)
	case "uuid-only":
		fmt.Printf("%s\n", uuid)
	case "timestamp-only":
		fmt.Printf("%s\n", timestamp)
	case "json":
		output := map[string]string{
			"id":        fmt.Sprintf("%s-%s-%s", timestamp, uuid, gitHash),
			"timestamp": timestamp,
			"uuid":      uuid,
			"git_hash":  gitHash,
		}
		enc := json.NewEncoder(os.Stdout)
		if err := enc.Encode(output); err != nil {
			return fmt.Errorf("encode json: %w", err)
		}
	default:
		return fmt.Errorf("unknown format: %s", *format)
	}

	return nil
}

// generateTimestamp generates a timestamp in YYYYMMDD-HHMMSS format.
func generateTimestamp() string {
	now := time.Now()
	return now.Format("20060102-150405")
}

// generateUUID generates a UUID v4 and returns the first 8 characters.
func generateUUID() (string, error) {
	// Generate 16 random bytes for UUID v4
	uuid := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, uuid); err != nil {
		return "", err
	}

	// Set version (4) and variant bits according to RFC 4122
	uuid[6] = (uuid[6] & 0x0f) | 0x40 // Version 4
	uuid[8] = (uuid[8] & 0x3f) | 0x80 // Variant is 10

	// Format as hex string and take first 8 characters
	return fmt.Sprintf("%02x%02x%02x%02x", uuid[0], uuid[1], uuid[2], uuid[3]), nil
}

// generateGitHash generates a hash of the git repository path.
// Returns "00000000" if -no-git flag is set or no git repo is found.
func generateGitHash() string {
	if *noGit {
		if *verbose {
			fmt.Fprintf(os.Stderr, "Git detection disabled\n")
		}
		return "00000000"
	}

	// Find git repository root
	gitRoot, err := findGitRoot()
	if err != nil {
		if *verbose {
			fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
		}
		// Fall back to CWD hash
		cwd, err := os.Getwd()
		if err != nil {
			if *verbose {
				fmt.Fprintf(os.Stderr, "Warning: could not get CWD: %v\n", err)
			}
			return "00000000"
		}
		gitRoot = cwd
		if *verbose {
			fmt.Fprintf(os.Stderr, "Falling back to CWD: %s\n", cwd)
		}
	} else {
		if *verbose {
			fmt.Fprintf(os.Stderr, "Git repo: %s\n", gitRoot)
		}
	}

	// Compute SHA-256 hash of the path
	hash := sha256.Sum256([]byte(gitRoot))
	hashStr := fmt.Sprintf("%x", hash)[:8]

	if *verbose {
		fmt.Fprintf(os.Stderr, "Repo hash: %s\n", hashStr)
	}

	return hashStr
}

// findGitRoot finds the root directory of the git repository.
// It traverses up from the current working directory looking for a .git directory.
func findGitRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}

	for {
		gitPath := filepath.Join(dir, ".git")
		if info, err := os.Stat(gitPath); err == nil {
			// Found .git - check if it's a directory or file (for worktrees)
			if info.IsDir() {
				return dir, nil
			}
			// .git file (worktree) - still use this directory as root
			return dir, nil
		}

		// Move up one directory
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached filesystem root without finding .git
			return "", fmt.Errorf("no git repository found")
		}
		dir = parent
	}
}
