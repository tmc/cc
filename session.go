package cc

import "github.com/tmc/cc/internal/ccmodel"

// The normalized session data model lives in internal/ccmodel so the
// per-format readers (codexread, opencoderead, piread) can construct entries
// without importing cc. These aliases keep the model spelled cc.Entry,
// cc.Message, and so on for all callers.
type (
	Entry               = ccmodel.Entry
	Attachment          = ccmodel.Attachment
	Message             = ccmodel.Message
	ContentBlock        = ccmodel.ContentBlock
	Usage               = ccmodel.Usage
	CacheCreationDetail = ccmodel.CacheCreationDetail
	CompactMetadata     = ccmodel.CompactMetadata
	ToolUseResult       = ccmodel.ToolUseResult
	FileResult          = ccmodel.FileResult
	TaskResult          = ccmodel.TaskResult
	TaskSummary         = ccmodel.TaskSummary
)

// MaxLineSize is the largest session line cc will read; tool-result payloads
// can be large.
const MaxLineSize = ccmodel.MaxLineSize

const initialBufferSize = ccmodel.InitialBufferSize

// ExtractAnyText returns text from string, block-array, or nested JSON values.
var ExtractAnyText = ccmodel.ExtractAnyText

// IsHarnessPreamble reports whether text is harness-supplied context rather
// than a prompt written by the user.
func IsHarnessPreamble(text string) bool { return ccmodel.IsHarnessPreamble(text) }

// PendingQuestions returns the AskUserQuestion tool_use blocks in entries
// that have no recorded answer, meaning the session is (or was) blocked at a
// question dialog. Antigravity persists pending dialogs, so a non-empty
// result there means the session is waiting now; Claude Code only flushes a
// question turn once it is answered, so its sessions never report pending
// questions from the file alone.
func PendingQuestions(entries []Entry) []ContentBlock {
	answered := make(map[string]bool)
	var asks []ContentBlock
	for _, e := range entries {
		if e.Message == nil {
			continue
		}
		for _, b := range e.Message.ContentBlocks() {
			switch b.Type {
			case "tool_use":
				if b.Name == "AskUserQuestion" || b.Name == "ask_question" {
					asks = append(asks, b)
				}
			case "tool_result":
				answered[b.ToolUseID] = true
			}
		}
	}
	var out []ContentBlock
	for _, a := range asks {
		if !answered[a.ID] {
			out = append(out, a)
		}
	}
	return out
}
