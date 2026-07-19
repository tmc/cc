package collector

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tmc/cc"
	"github.com/tmc/cc/cass"
)

// claudeLinkPattern matches the claude.ai links a session may reference: an
// artifact published by the Artifact tool, a shared public artifact, a Claude
// Code session link (in commit messages and PR bodies), or a design link. The
// trailing character class stops at the first delimiter so surrounding quotes,
// parentheses, and backticks are not captured.
var claudeLinkPattern = regexp.MustCompile(
	`https://claude\.ai/(?:code/artifact/[A-Za-z0-9-]+|public/artifacts/[A-Za-z0-9-]+|code/session_[A-Za-z0-9]+|design/[A-Za-z0-9_-]+)`)

// ExtractArtifacts collects claude.ai artifacts and links referenced by a
// session. Two sources are scanned:
//
//   - Artifact tool results, which carry the published artifact URL along with
//     the local source path and title (from Entry.ToolUseResult).
//   - claude.ai links mentioned in assistant text or Bash commands, such as
//     session and design links pasted into commit messages or PR bodies.
//
// Each Artifact tool result is a publication event and is retained. Incidental
// text and command references are deduplicated by URL.
func ExtractArtifacts(entries []cc.Entry) []cass.Artifact {
	var artifacts []cass.Artifact
	seen := make(map[string]bool)

	addReference := func(a cass.Artifact) {
		if a.URL == "" || seen[a.URL] {
			return
		}
		seen[a.URL] = true
		artifacts = append(artifacts, a)
	}

	for i, e := range entries {
		ts := ""
		if !e.Timestamp.IsZero() {
			ts = e.Timestamp.Format(tsFormat)
		}

		// Artifact tool result: the canonical, fully-populated source.
		if r := e.ToolUseResult; r != nil {
			if kind := classifyArtifact(r.URL); kind != "" {
				artifacts = append(artifacts, cass.Artifact{
					EventID:   artifactEventID(e.UUID, i),
					Kind:      kind,
					URL:       r.URL,
					ID:        artifactID(r.URL),
					Path:      r.Path,
					Title:     r.Title,
					Source:    "tool_result",
					Timestamp: ts,
				})
			}
		}

		if e.Message == nil {
			continue
		}
		// Assistant text and Bash commands may mention links (e.g. a session
		// link in a commit message). User messages are skipped to avoid
		// echoing links the user pasted rather than the session produced.
		if e.Message.Role != "assistant" {
			continue
		}
		for _, b := range e.Message.ContentBlocks() {
			switch {
			case b.Type == "text":
				addLinks(addReference, b.Text, "text", ts)
			case b.Type == "tool_use" && b.Name == "Bash":
				addLinks(addReference, extractBashCommand(b.Input), "command", ts)
			}
		}
	}
	return artifacts
}

func artifactEventID(uuid string, ordinal int) string {
	if uuid != "" {
		return uuid
	}
	return fmt.Sprintf("entry-%d", ordinal)
}

// addLinks finds claude.ai links in text and passes each to add.
func addLinks(add func(cass.Artifact), text, source, ts string) {
	for _, url := range claudeLinkPattern.FindAllString(text, -1) {
		add(cass.Artifact{
			Kind:      classifyArtifact(url),
			URL:       url,
			ID:        artifactID(url),
			Source:    source,
			Timestamp: ts,
		})
	}
}

// classifyArtifact returns the artifact kind for a claude.ai URL, or "" if the
// URL is not a recognized claude.ai artifact, session, or design link. The host
// check keeps a tool result whose URL happens to contain a matching path
// segment (e.g. a fetched third-party URL) from being classified as an artifact.
func classifyArtifact(url string) string {
	if !strings.Contains(url, "claude.ai/") {
		return ""
	}
	switch {
	case strings.Contains(url, "/code/artifact/"):
		return cass.ArtifactKindArtifact
	case strings.Contains(url, "/public/artifacts/"):
		return cass.ArtifactKindPublic
	case strings.Contains(url, "/code/session_"):
		return cass.ArtifactKindSession
	case strings.Contains(url, "/design/"):
		return cass.ArtifactKindDesign
	}
	return ""
}

// artifactID returns the identifier segment of a claude.ai URL: the UUID for an
// artifact, the session_<id> for a session link, or the slug for a design link.
func artifactID(url string) string {
	for _, prefix := range []string{"/code/artifact/", "/public/artifacts/", "/code/", "/design/"} {
		if _, rest, ok := strings.Cut(url, prefix); ok {
			if j := strings.IndexAny(rest, "/?#"); j >= 0 {
				rest = rest[:j]
			}
			return rest
		}
	}
	return ""
}
