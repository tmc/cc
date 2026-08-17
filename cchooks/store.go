package cchooks

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tmc/cc/cass/it2bind"
)

// eventsFile is where a registered hook handler appends, relative to a session
// directory. It is it2bind's constant; cchooks needs the name to tell a
// directory that was never written from one whose contents cannot be read.
const eventsFile = "artifacts/claude-code-events.ndjson"

// StoreStats counts an event store at each stage a record must survive to
// become a pane binding. The counts narrow: every line was written into some
// directory, only some parse, only some of those carry both identifiers, and
// only some of those group into an interval. Whichever count first falls to
// zero names the broken stage.
type StoreStats struct {
	Root      string
	Dirs      int // session directories present
	Twinned   int // canonical ids holding more than one directory
	Files     int // directories carrying an events file
	Lines     int // non-blank lines in those files
	Bound     int // lines carrying both a session id and an it2 session id
	Intervals int // bindings a consumer resolves
}

// Store diagnostic codes.
const (
	CodeStoreAbsent     = "store-absent"
	CodeStoreUnwritten  = "store-unwritten"
	CodeStoreEmpty      = "store-empty"
	CodeStoreUnbound    = "store-unbound"
	CodeStoreUnresolved = "store-unresolved"
	CodeStoreTwinned    = "store-twinned"
)

// ReadStore counts the event store under root and reports the first stage that
// yields nothing. Pass "" to use the default root.
//
// It exists because the readers over this store answer zero the same way they
// answer a real result, so a store that has never held one usable record is
// indistinguishable at every call site from one that simply does not apply
// right now. That silence held for nine months here. A count that reaches zero
// is a finding, and this reports it as one.
func ReadStore(root string) (StoreStats, []Diagnostic) {
	if root == "" {
		r, ok := it2bind.DefaultRoot()
		if !ok {
			return StoreStats{}, nil
		}
		root = r
	}
	st := StoreStats{Root: root}
	layer := Layer{Agent: "claude-code", Path: root}
	diag := func(code, sev, msg string) Diagnostic {
		return Diagnostic{Code: code, Severity: sev, Message: msg, Layer: layer}
	}

	if _, err := os.Stat(root); err != nil {
		// No store is not a fault: nothing here is installed, and this package
		// reports an uninstalled thing as absent rather than broken.
		return st, []Diagnostic{diag(CodeStoreAbsent, "info", "no event store at "+root+"; no handler has ever written one")}
	}

	dirs, err := it2bind.DiscoverDirs(root)
	if err != nil {
		return st, []Diagnostic{diag(CodeStoreAbsent, "error", "cannot read event store: "+err.Error())}
	}
	st.Dirs = len(dirs)
	for _, paths := range dirs {
		if len(paths) > 1 {
			st.Twinned++
		}
		for _, dir := range paths {
			path := filepath.Join(dir, eventsFile)
			n, err := countLines(path)
			if err != nil {
				continue
			}
			st.Files++
			st.Lines += n
			events, err := it2bind.ReadEvents(path)
			if err != nil {
				continue
			}
			st.Bound += len(events)
		}
	}
	if intervals, err := it2bind.Load(root); err == nil {
		st.Intervals = len(intervals)
	}

	return st, st.diagnose(diag)
}

// diagnose reports the first stage that yields nothing, and stops. Reporting
// every empty stage would restate one failure four times: a store nothing
// writes to is also unparseable, unbound, and unresolved.
func (st StoreStats) diagnose(diag func(code, sev, msg string) Diagnostic) []Diagnostic {
	var out []Diagnostic
	if st.Twinned > 0 {
		out = append(out, diag(CodeStoreTwinned, "warn", fmt.Sprintf(
			"%d session ids hold more than one directory: a writer stamped the wNtNpN: pane-position prefix, which is a location and goes stale when a pane moves. Fix the writer; do not merge the directories. A reader that canonicalizes already folds both, and the twins hold files of the same name, so moving one onto the other destroys events", st.Twinned)))
	}
	switch {
	case st.Dirs == 0:
		out = append(out, diag(CodeStoreUnwritten, "info", "event store exists but holds no session directories"))
	case st.Files == 0:
		out = append(out, diag(CodeStoreUnwritten, "error", fmt.Sprintf(
			"%d session directories, none holding %s: no handler is registered, or the registered command is not on PATH", st.Dirs, eventsFile)))
	case st.Lines == 0:
		out = append(out, diag(CodeStoreEmpty, "error", fmt.Sprintf(
			"%d event files, all empty: the handler is registered and runs but writes nothing", st.Files)))
	case st.Bound == 0:
		out = append(out, diag(CodeStoreUnbound, "error", fmt.Sprintf(
			"%d lines across %d files, none carrying both a session id and an it2 session id: the writer predates the binding field, so every record is unusable to a reader", st.Lines, st.Files)))
	case st.Intervals == 0:
		out = append(out, diag(CodeStoreUnresolved, "error", fmt.Sprintf(
			"%d bound events but no intervals: records parse and carry both ids yet group into no binding", st.Bound)))
	}
	return out
}

// countLines returns the number of non-blank lines in a file.
func countLines(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) != "" {
			n++
		}
	}
	return n, sc.Err()
}
