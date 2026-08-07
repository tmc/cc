package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/term"
)

// pagerCommand resolves the pager the way git does: CCRENDER_PAGER, then
// PAGER, then less. A setting that is empty or blank means "no pager". look
// reports an environment variable, as os.LookupEnv does.
func pagerCommand(look func(string) (string, bool)) []string {
	for _, env := range []string{"CCRENDER_PAGER", "PAGER"} {
		if v, ok := look(env); ok {
			if strings.TrimSpace(v) == "" {
				return nil
			}
			// The setting is a command line, not a program name: PAGER is
			// conventionally something like "less -R".
			return []string{"sh", "-c", v}
		}
	}
	return []string{"less"}
}

// runPager renders text through the user's pager, which is where transcript
// navigation lives: less already has search, marks, and line jumps, and
// reimplementing them in the viewport would be a worse less.
//
// Like git, ccrender defaults LESS to FRX — quit if the text fits on one
// screen, keep ANSI color, and don't wipe the screen on exit — but leaves an
// existing LESS alone, since that is the user's own configuration.
func runPager(text string) error {
	argv := pagerCommand(os.LookupEnv)
	if len(argv) == 0 {
		_, err := io.WriteString(os.Stdout, text)
		return err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = strings.NewReader(text)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if _, ok := os.LookupEnv("LESS"); !ok {
		cmd.Env = append(os.Environ(), "LESS=FRX")
	}
	if err := cmd.Run(); err != nil {
		// Quitting the pager early closes the pipe; that is how a reader
		// leaves, not a failure.
		var exit *exec.ExitError
		if errors.As(err, &exit) || errors.Is(err, io.ErrClosedPipe) {
			return nil
		}
		return fmt.Errorf("%s: %w", argv[0], err)
	}
	return nil
}

// termWidth reports the width to wrap to: the terminal's own width, else
// $COLUMNS, else a terminal-typical 100.
func termWidth() int {
	if w, _, err := term.GetSize(os.Stdout.Fd()); err == nil && w > 0 {
		return w
	}
	if n, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && n > 0 {
		return n
	}
	return 100
}
