package cc

import "os"

// resolvePersistedOutput restores tool output that Claude Code spilled to a
// sidecar file under <session>/tool-results/. The transcript keeps a preview of
// roughly two kilobytes, and toolUseResult.stdout is itself capped, so a reader
// that trusts either one sees a fraction of what the tool printed.
//
// A missing sidecar leaves the entry untouched: transcripts outlive their
// sidecars, and travel to machines where the recorded absolute path means
// nothing. Rendering the preview is better than failing the read.
func resolvePersistedOutput(e *Entry) {
	tur := e.ToolUseResult
	if tur == nil || tur.PersistedOutputPath == "" {
		return
	}
	data, err := os.ReadFile(tur.PersistedOutputPath)
	if err != nil {
		return
	}
	tur.Stdout = string(data)
}
