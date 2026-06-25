// Package ccrun runs Claude Code once in headless print mode.
//
// It invokes the Claude Code CLI with JSON output, parses the result envelope,
// and, when possible, reads the session transcript from the local Claude Code
// project store.
package ccrun
