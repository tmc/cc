package cc

import "strings"

// ApplyPatchPaths returns the paths named by Add File, Update File, Delete
// File, and Move to headers in apply_patch text. Paths are deduplicated in
// encounter order. They are not resolved against a directory. This extracts
// declared targets; it does not prove that a patch was valid or applied.
func ApplyPatchPaths(text string) []string {
	var paths []string
	seen := make(map[string]bool)
	for _, line := range strings.Split(text, "\n") {
		for _, prefix := range []string{"*** Add File: ", "*** Update File: ", "*** Delete File: ", "*** Move to: "} {
			if path, ok := strings.CutPrefix(line, prefix); ok {
				path = strings.TrimSpace(path)
				if path != "" && !seen[path] {
					seen[path] = true
					paths = append(paths, path)
				}
				break
			}
		}
	}
	return paths
}
