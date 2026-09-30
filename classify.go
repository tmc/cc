package cc

import "strings"

// ClassificationRule returns a message origin category, or "" if it does not
// match. Categories are human, peer, subagent, system, tool, and command.
type ClassificationRule func(Entry) string

// ClassificationRules returns the default rules in precedence order: explicit
// origin, tool result, task notification, sidechain, teammate message, command, harness
// context, and user prompt. Each call returns a fresh slice. Consumers can
// prepend their own rules and apply the first rule returning a nonempty value.
// The collab-msg protocol is deliberately left to consumers.
func ClassificationRules() []ClassificationRule {
	return []ClassificationRule{
		classifyOrigin,
		classifyTool,
		classifyTask,
		classifySubagent,
		classifyPeer,
		classifyCommand,
		classifySystem,
		classifyHuman,
	}
}

// Classify reports the origin of a message using [ClassificationRules].
// It returns "" for entries without a recognized origin or message shape,
// including ordinary assistant responses. It does not count assistant tool
// calls as tool results.
func Classify(e Entry) string {
	for _, rule := range ClassificationRules() {
		if kind := rule(e); kind != "" {
			return kind
		}
	}
	return ""
}

func classifyOrigin(e Entry) string {
	switch e.Origin {
	case "human", "peer", "subagent", "system", "tool", "command":
		return e.Origin
	case "task-notification":
		return "subagent"
	case "auto-continuation":
		return "system"
	}
	return ""
}

func classificationText(e Entry) string {
	if e.Message != nil {
		return strings.TrimSpace(e.Message.TextContent())
	}
	return strings.TrimSpace(e.Content)
}

func classifyTool(e Entry) string {
	if e.Type == "tool" || (e.Message != nil && (e.Message.Role == "tool" || e.Message.IsToolResultOnly())) {
		return "tool"
	}
	return ""
}

func classifyTask(e Entry) string {
	if _, ok := ParseTaskNotification(classificationText(e)); ok {
		return "subagent"
	}
	return ""
}

func classifySubagent(e Entry) string {
	if e.IsSidechain || e.AgentID != "" || e.ParentThreadID != "" {
		return "subagent"
	}
	return ""
}

func classifyPeer(e Entry) string {
	text := classificationText(e)
	if strings.HasPrefix(text, "<teammate-message>") || strings.HasPrefix(text, "<teammate-message ") {
		return "peer"
	}
	return ""
}

func classifyCommand(e Entry) string {
	text := classificationText(e)
	for _, prefix := range []string{"<command-name>", "<command-message>", "<command-args>", "<local-command-stdout>"} {
		if strings.HasPrefix(text, prefix) {
			return "command"
		}
	}
	return ""
}

func classifySystem(e Entry) string {
	if e.IsMeta || e.IsCompactSummary || e.Type == "system" || (e.Message != nil && e.Message.Role == "system") || IsHarnessPreamble(classificationText(e)) {
		return "system"
	}
	return ""
}

func classifyHuman(e Entry) string {
	if e.Message != nil && e.Message.Role == "user" && classificationText(e) != "" {
		return "human"
	}
	return ""
}
