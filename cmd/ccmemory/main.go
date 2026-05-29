package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tmc/cc/ccpaths"
)

var (
	projectFlag = flag.String("project", "", "Target project directory (default: current directory)")
	showFlag    = flag.String("show", "", "Print the memory file with the given name")
	indexFlag   = flag.Bool("index", false, "Print MEMORY.md")
	pathFlag    = flag.Bool("path", false, "Print the memory directory and exit")
)

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "ccmemory: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	dir, err := memoryDir(*projectFlag)
	if err != nil {
		return err
	}

	if *pathFlag {
		fmt.Println(dir)
		return nil
	}

	if *indexFlag {
		return catFile(filepath.Join(dir, "MEMORY.md"), os.Stdout)
	}

	if *showFlag != "" {
		return showMemory(dir, *showFlag, os.Stdout)
	}

	return listMemories(dir, os.Stdout)
}

// memoryDir returns the memory directory for the given project (or cwd).
// If the directly-encoded path has no memory/ directory, it retries with the
// symlink-resolved path so callers behind symlinks (e.g. /Users/x → /Volumes/x)
// still find the canonical location.
func memoryDir(project string) (string, error) {
	if project == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		project = cwd
	}
	abs, err := filepath.Abs(project)
	if err != nil {
		return "", fmt.Errorf("resolve project: %w", err)
	}
	home, err := ccpaths.ClaudeHome()
	if err != nil {
		return "", err
	}
	candidate := filepath.Join(home, "projects", ccpaths.EncodePath(abs), "memory")
	if _, err := os.Stat(candidate); err == nil {
		return candidate, nil
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil && resolved != abs {
		return filepath.Join(home, "projects", ccpaths.EncodePath(resolved), "memory"), nil
	}
	return candidate, nil
}

func listMemories(dir string, w io.Writer) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no memories: %s", dir)
		}
		return err
	}
	var names []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".md") || name == "MEMORY.md" {
			continue
		}
		names = append(names, strings.TrimSuffix(name, ".md"))
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintln(w, n)
	}
	return nil
}

func showMemory(dir, name string, w io.Writer) error {
	if !strings.HasSuffix(name, ".md") {
		name += ".md"
	}
	return catFile(filepath.Join(dir, name), w)
}

func catFile(path string, w io.Writer) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}
