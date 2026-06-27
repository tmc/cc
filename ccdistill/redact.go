package ccdistill

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
)

// redactRule is one redaction pattern and its fixed replacement. The set is
// deliberately conservative: every pattern anchors on a documented prefix or
// structural marker so it never substring-matches inside ordinary identifiers
// or code. Order is significant; see redactRules.
type redactRule struct {
	re   *regexp.Regexp
	repl string
}

// redactRules is the fixed redaction table, applied in order. Private-key
// blocks run first (multiline), then API keys/tokens (specific prefixes before
// the generic sk- form), then bearer/authorization headers, then email
// addresses. The home-directory rewrite is handled separately and runs last so
// it does not eat into a key match. Patterns are case-sensitive unless the
// expression sets (?i).
var redactRules = []redactRule{
	// Private keys (multiline, DOTALL) — must run before per-line patterns.
	{regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`), "[REDACTED_PRIVATE_KEY]"},

	// API keys / tokens. Specific OpenAI/Anthropic prefixes run before the
	// generic sk- form so they win the match.
	{regexp.MustCompile(`sk-proj-[A-Za-z0-9_-]{20,}`), "[REDACTED_API_KEY]"},
	{regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{20,}`), "[REDACTED_API_KEY]"},
	{regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`), "[REDACTED_API_KEY]"},
	{regexp.MustCompile(`AKIA[0-9A-Z]{16}`), "[REDACTED_AWS_KEY]"},
	{regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{36,255}`), "[REDACTED_GITHUB_TOKEN]"},
	{regexp.MustCompile(`AIza[0-9A-Za-z_-]{35}`), "[REDACTED_GOOGLE_KEY]"},
	{regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`), "[REDACTED_SLACK_TOKEN]"},

	// Bearer / authorization headers: preserve the keyword, scrub the value.
	{regexp.MustCompile(`(?i)\b(authorization|bearer)\b[:= ]+\S+`), "$1 [REDACTED_TOKEN]"},

	// Email addresses.
	{regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`), "[REDACTED_EMAIL]"},
}

// Redact scrubs secrets and PII from s in place and returns the number of
// substitutions made. It rewrites every text field — Block.Text, ToolUse.Input
// (as bytes), ToolResult.Content, and the Meta.Source and Meta.CWD paths — so a
// distilled Sample carries no API keys, tokens, private keys, email addresses,
// or absolute home paths.
//
// Redact is conservative so it does not corrupt code: every pattern anchors on a
// documented prefix or structural marker. It scrubs OpenAI/Anthropic sk- keys,
// AWS AKIA keys, GitHub gh*_ tokens, Google AIza keys, Slack xox*- tokens,
// Bearer/Authorization header values, PEM private-key blocks, and email
// addresses, each to a fixed [REDACTED_*] token. UUIDs, git SHAs, plain numbers,
// IP addresses, and credential-free URLs are deliberately left intact.
func Redact(s *Sample) int {
	if s == nil {
		return 0
	}
	n := 0
	for i := range s.Turns {
		for j := range s.Turns[i].Blocks {
			b := &s.Turns[i].Blocks[j]
			if b.Text != "" {
				b.Text, n = redactInto(b.Text, n)
			}
			if b.ToolUse != nil && len(b.ToolUse.Input) > 0 {
				b.ToolUse.Input, n = redactRaw(b.ToolUse.Input, n)
			}
			if b.ToolRes != nil && b.ToolRes.Content != "" {
				b.ToolRes.Content, n = redactInto(b.ToolRes.Content, n)
			}
		}
	}
	s.Meta.Source, n = redactInto(s.Meta.Source, n)
	s.Meta.CWD, n = redactInto(s.Meta.CWD, n)
	return n
}

// redactInto applies the redaction engine to s and adds its substitution count
// to acc, returning the redacted string and the new running total.
func redactInto(s string, acc int) (string, int) {
	out, c := redactCount(s)
	return out, acc + c
}

// redactRaw scrubs a tool_use Input, which the anthropic encoder emits verbatim
// as a json.RawMessage. When raw is valid JSON, it redacts the decoded string
// values and re-marshals, so the result is always valid JSON — textual
// substitution on the raw bytes can split a "\\" escape pair and produce
// invalid JSON the encoder then fails to marshal. For a non-JSON input it falls
// back to plain textual redaction. It returns the redacted bytes and the new
// running substitution total.
func redactRaw(raw json.RawMessage, acc int) (json.RawMessage, int) {
	if json.Valid(raw) {
		var v any
		if err := json.Unmarshal(raw, &v); err == nil {
			n := 0
			v = redactValue(v, &n)
			if out, err := json.Marshal(v); err == nil {
				return out, acc + n
			}
		}
	}
	out, n := redactInto(string(raw), acc)
	return json.RawMessage(out), n
}

// redactValue walks a decoded JSON value and redacts every string in place,
// adding the substitution count to n. Object keys are left intact (they are
// argument names, not secret-bearing values).
func redactValue(v any, n *int) any {
	switch x := v.(type) {
	case string:
		out, c := redactCount(x)
		*n += c
		return out
	case []any:
		for i, e := range x {
			x[i] = redactValue(e, n)
		}
		return x
	case map[string]any:
		for k, e := range x {
			x[k] = redactValue(e, n)
		}
		return x
	default:
		return v
	}
}

// redactCount applies the conservative redaction table to s and rewrites the
// current user's absolute home-directory prefix to "~". It returns the redacted
// string and the number of substitutions made. The home rewrite runs last so it
// cannot truncate a key or token match. It backs the Sample-level pipeline,
// which reports the substitution count on stderr.
func redactCount(s string) (string, int) {
	n := 0
	for _, r := range redactRules {
		s = r.re.ReplaceAllStringFunc(s, func(m string) string {
			n++
			// ReplaceAllStringFunc does not expand $1, so for the
			// bearer/authorization rule we reconstruct the keyword
			// from the submatch and append the redacted value.
			if sub := r.re.FindStringSubmatch(m); len(sub) > 1 && strings.Contains(r.repl, "$1") {
				return strings.Replace(r.repl, "$1", sub[1], 1)
			}
			return r.repl
		})
	}
	if home := homeDir(); home != "" {
		if c := strings.Count(s, home); c > 0 {
			s = strings.ReplaceAll(s, home, "~")
			n += c
		}
	}
	return s, n
}

// homeDir returns the current user's home directory, or "" when it cannot be
// resolved (in which case the home-path rewrite is skipped). A trailing path
// separator is trimmed so the prefix matches both the bare directory and paths
// beneath it.
func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return strings.TrimRight(h, "/")
}
