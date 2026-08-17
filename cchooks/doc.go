// Package cchooks reads the hook configuration of coding agents.
//
// Hooks are commands an agent runs at points in its lifecycle. They are
// configured in several files per agent, layered so that a later layer adds to
// (never replaces) an earlier one, and a handler installed by a plugin is
// indistinguishable at runtime from one a person wrote by hand. The result is
// easy to break silently: a handler can be present, well-formed, and never run.
//
// This package reads every layer and reports what is installed, where it came
// from, and whether it can work. It never writes. Callers that install hooks
// are expected to use [Read] first and to preserve everything it reports with
// provenance [Manual].
//
// The unit of configuration is a [Handler]. Handlers are grouped under an event
// name and, for tool events, filtered by a matcher. [Diagnose] reports the
// failure modes that static reading can catch, including the ones that produce
// a handler that installs cleanly and never fires.
package cchooks
