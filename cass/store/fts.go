package store

import "strings"

// ftsQuery converts a user search string into a valid FTS5 query.
//
// Bare words keep the usual FTS AND semantics. Terms containing path, URL, or
// other punctuation are quoted so FTS tokenizes them as text instead of syntax.
func ftsQuery(q string) string {
	fields := strings.Fields(q)
	if len(fields) == 0 {
		return ""
	}
	for i, f := range fields {
		if needsFTSQuote(f) {
			fields[i] = `"` + strings.ReplaceAll(f, `"`, `""`) + `"`
		}
	}
	return strings.Join(fields, " ")
}

func needsFTSQuote(s string) bool {
	for _, r := range s {
		switch {
		case 'a' <= r && r <= 'z':
		case 'A' <= r && r <= 'Z':
		case '0' <= r && r <= '9':
		case r == '_':
		default:
			return true
		}
	}
	return false
}
