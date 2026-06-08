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
