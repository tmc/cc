package collector

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tmc/cc"
	"github.com/tmc/cc/cass"
)

// agentFilePrefix is the leading filename component for subagent JSONLs.
const agentFilePrefix = "agent-"

// agentCompactPrefix marks compaction subagents that supplement the
// primary agent JSONL. They are emitted as SubagentRun records with
// IsCompaction set, but excluded from parent FTS merging and graph fan-out.
const agentCompactPrefix = "agent-acompact"

// extractSubagentRuns builds SubagentRun records for each agent-<id>.jsonl
// found under <sessionPath-without-suffix>/subagents/. It cross-references
// queue-operation entries from the parent session for authoritative
// timing/token data.
//
// parentEntries are expected to be the parent session's own entries (no
// merged subagent entries) so queue-operation pairing is unambiguous.
//
// Subagent JSONLs that lack a matching queue-operation notification still
// produce a SubagentRun with Status="unknown" and zero usage. Existence
// of the file is itself informative.
func extractSubagentRuns(ctx context.Context, sessionPath string, parentEntries []cc.Entry, parentSession cass.Session) []cass.SubagentRun {
	subagentDir := filepath.Join(strings.TrimSuffix(sessionPath, ".jsonl"), "subagents")
	infos, err := os.ReadDir(subagentDir)
	if err != nil {
		return nil
	}

	notes := indexTaskNotifications(parentEntries)

	var runs []cass.SubagentRun
	for _, fi := range infos {
		name := fi.Name()
		if fi.IsDir() {
			continue
		}
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		if !strings.HasPrefix(name, agentFilePrefix) {
			continue
		}
		isCompaction := strings.HasPrefix(name, agentCompactPrefix)
		agentID := strings.TrimSuffix(strings.TrimPrefix(name, agentFilePrefix), ".jsonl")
		if agentID == "" {
			continue
		}
		runPath := filepath.Join(subagentDir, name)
		entries, err := cc.ReadFile(ctx, runPath)
		if err != nil {
			continue
		}

		run := cass.SubagentRun{
			AgentID:         agentID,
			ParentSessionID: parentSession.ID,
			ParentClaudeSID: claudeSessionID(entries),
			Workspace:       parentSession.Workspace,
			GitCommonDir:    parentSession.GitCommonDir,
			SourcePath:      runPath,
			EntryCount:      len(entries),
			Status:          "unknown",
			IsCompaction:    isCompaction,
		}
		populateRunFromEntries(&run, entries)

		metaPath := filepath.Join(subagentDir, agentFilePrefix+agentID+".meta.json")
		if meta, err := cc.ReadSubagentMeta(metaPath); err == nil {
			run.AgentType = meta.AgentType
			run.Description = meta.Description
			if _, statErr := os.Stat(metaPath); statErr == nil {
				run.MetaPath = metaPath
			}
		}

		if note, ok := notes[agentID]; ok {
			run.EnqueuedAt = note.enqueuedAt
			run.DequeuedAt = note.dequeuedAt
			run.ToolUseID = note.notification.ToolUseID
			run.OutputFile = note.notification.OutputFile
			run.WorktreePath = note.notification.WorktreePath
			run.WorktreeBranch = note.notification.WorktreeBranch
			run.TotalTokens = note.notification.TotalTokens
			run.ToolUses = note.notification.ToolUses
			run.DurationMs = note.notification.DurationMs
			if s := note.notification.Status; s != "" {
				run.Status = s
			}
		}

		runs = append(runs, run)
	}

	sort.Slice(runs, func(i, j int) bool {
		return runs[i].StartedAt.Before(runs[j].StartedAt)
	})
	return runs
}

// populateRunFromEntries fills StartedAt, EndedAt, and Model from the
// subagent's own JSONL entries.
func populateRunFromEntries(run *cass.SubagentRun, entries []cc.Entry) {
	for _, e := range entries {
		if !e.Timestamp.IsZero() {
			if run.StartedAt.IsZero() {
				run.StartedAt = e.Timestamp
			}
			run.EndedAt = e.Timestamp
		}
		if run.Model == "" && e.Message != nil && e.Message.Model != "" {
			run.Model = e.Message.Model
		}
	}
}

// claudeSessionID returns the parent's Claude sessionId from the first
// entry that carries one. Subagent entries always carry the parent's
// sessionId per DATAMODEL §V3.
func claudeSessionID(entries []cc.Entry) string {
	for _, e := range entries {
		if e.SessionID != "" {
			return e.SessionID
		}
	}
	return ""
}

// taskNotificationRecord pairs a parsed notification with the timestamps
// of its enqueue and the next-following dequeue/remove operation.
type taskNotificationRecord struct {
	notification cc.TaskNotification
	enqueuedAt   time.Time
	dequeuedAt   time.Time
}

// indexTaskNotifications walks the parent's queue-operation events,
// pairs each enqueue carrying a <task-notification> with the next
// following queue-operation timestamp, and returns the records keyed
// by task-id (== agentId).
//
// Pairing rule: dequeue/remove consume one pending notification, and
// popAll consumes every pending notification at that timestamp. Concurrent
// notifications are paired in FIFO order.
//
// Non-notification enqueues (free-text human input) are skipped at the
// ParseTaskNotification step, so they neither create nor consume queue
// records.
func indexTaskNotifications(entries []cc.Entry) map[string]taskNotificationRecord {
	out := make(map[string]taskNotificationRecord)

	type pending struct {
		taskID string
	}
	var queue []pending
	consumeOne := func(ts time.Time) {
		if len(queue) == 0 {
			return
		}
		head := queue[0]
		queue = queue[1:]
		rec := out[head.taskID]
		rec.dequeuedAt = ts
		out[head.taskID] = rec
	}

	for _, e := range entries {
		if e.Type != "queue-operation" {
			continue
		}

		switch e.Operation {
		case "enqueue", "":
			notif, ok := cc.ParseTaskNotification(e.Content)
			if ok && notif.TaskID != "" {
				rec := taskNotificationRecord{
					notification: notif,
					enqueuedAt:   e.Timestamp,
				}
				out[notif.TaskID] = rec
				queue = append(queue, pending{taskID: notif.TaskID})
			}
			if e.Operation != "" {
				continue
			}
			// Legacy entries without an operation field used the next
			// queue-operation as the consume signal.
			if ok {
				continue
			}
			consumeOne(e.Timestamp)
		case "dequeue", "remove":
			consumeOne(e.Timestamp)
		case "popAll":
			for len(queue) > 0 {
				consumeOne(e.Timestamp)
			}
		}
	}
	return out
}
