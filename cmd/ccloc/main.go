package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tmc/cc/ccpaths"
)

var (
	harnessFlag  = flag.String("harness", "", "Harness to resolve for (claude, gemini, codex, opencode, pi, antigravity)")
	homeFlag     = flag.Bool("home", false, "Print the harness data home instead of a project directory")
	rootFlag     = flag.Bool("root", false, "Print the harness sessions root instead of a project directory")
	checkFlag    = flag.Bool("check", false, "Check if the directory exists")
	createFlag   = flag.Bool("create", false, "Create the directory if needed")
	sessionsFlag = flag.Bool("sessions", false, "Print the sessions subdirectory of the project directory")
	shortFlag    = flag.Bool("short", false, "Use ~ instead of full home path")

	claudeFlag      = flag.Bool("claude", false, "Shorthand for -harness claude")
	geminiFlag      = flag.Bool("gemini", false, "Shorthand for -harness gemini")
	codexFlag       = flag.Bool("codex", false, "Shorthand for -harness codex")
	opencodeFlag    = flag.Bool("opencode", false, "Shorthand for -harness opencode")
	piFlag          = flag.Bool("pi", false, "Shorthand for -harness pi")
	antigravityFlag = flag.Bool("antigravity", false, "Shorthand for -harness antigravity")
)

func main() {
	flag.Parse()

	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "ccloc: %v\n", err)
		os.Exit(1)
	}
}

// selectedHarness resolves -harness and the shorthand flags to one name,
// defaulting to claude. Naming more than one harness is an error.
func selectedHarness() (string, error) {
	shorthands := []struct {
		set  bool
		name string
	}{
		{*claudeFlag, "claude"},
		{*geminiFlag, "gemini"},
		{*codexFlag, "codex"},
		{*opencodeFlag, "opencode"},
		{*piFlag, "pi"},
		{*antigravityFlag, "antigravity"},
	}
	harness := *harnessFlag
	for _, s := range shorthands {
		if !s.set {
			continue
		}
		if harness != "" && harness != s.name {
			return "", fmt.Errorf("conflicting harness flags: %q and %q", harness, s.name)
		}
		harness = s.name
	}
	if harness == "" {
		harness = "claude"
	}
	return harness, nil
}

func run() error {
	harness, err := selectedHarness()
	if err != nil {
		return err
	}

	var path string
	switch {
	case *homeFlag:
		path, err = ccpaths.DataHome(harness)
	case *rootFlag:
		path, err = ccpaths.SessionsRoot(harness)
	default:
		dir := "."
		if flag.NArg() > 0 {
			dir = flag.Arg(0)
		}
		absDir, absErr := filepath.Abs(dir)
		if absErr != nil {
			return fmt.Errorf("resolve path: %w", absErr)
		}
		path, err = ccpaths.ProjectDir(harness, absDir)
		if *sessionsFlag && err == nil {
			path = filepath.Join(path, "sessions")
		}
	}
	if err != nil {
		return err
	}

	if *checkFlag {
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("directory does not exist: %s", path)
		}
		return nil
	}
	if *createFlag {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return fmt.Errorf("create directory: %w", err)
		}
	}

	if *shortFlag {
		home, err := os.UserHomeDir()
		if err == nil && home != "" {
			path = strings.Replace(path, home, "~", 1)
		}
	}
	fmt.Println(path)
	return nil
}
