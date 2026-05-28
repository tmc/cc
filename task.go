package cc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

// TeamTask represents a task stored at ~/.claude/tasks/{namespace}/{id}.json.
// Named TeamTask to avoid collision with the session-level TaskResult type.
type TeamTask struct {
	ID          string         `json:"id"`
	Subject     string         `json:"subject"`
	Description string         `json:"description"`
	ActiveForm  string         `json:"activeForm,omitempty"`
	Owner       string         `json:"owner,omitempty"`
	Status      string         `json:"status"` // pending, in_progress, completed
	Blocks      []string       `json:"blocks"`
	BlockedBy   []string       `json:"blockedBy"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// TaskStore manages task files under a namespace directory.
type TaskStore struct {
	dir string
}

// NewTaskStore creates a TaskStore for the given namespace (typically a team name).
// If CC_TASKS_DIR is set, it is used as the base; otherwise defaults to ~/.claude/tasks/.
func NewTaskStore(ctx context.Context, namespace string) (*TaskStore, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	base := os.Getenv("CC_TASKS_DIR")
	if base == "" {
		ch, err := ClaudeHome()
		if err != nil {
			return nil, fmt.Errorf("task store: %w", err)
		}
		base = filepath.Join(ch, "tasks")
	}
	dir := filepath.Join(base, namespace)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create task dir: %w", err)
	}
	return &TaskStore{dir: dir}, nil
}

// Create creates a new task with an auto-incremented ID.
// Holds the store-level lock for the full allocate-then-write sequence so
// concurrent callers never collide on the same ID.
func (s *TaskStore) Create(t TeamTask) (TeamTask, error) {
	lock, err := s.lockStore()
	if err != nil {
		return t, err
	}
	defer lock()

	id, err := s.nextIDLocked()
	if err != nil {
		return t, err
	}
	t.ID = strconv.Itoa(id)
	if t.Status == "" {
		t.Status = "pending"
	}
	if t.Blocks == nil {
		t.Blocks = []string{}
	}
	if t.BlockedBy == nil {
		t.BlockedBy = []string{}
	}
	if err := s.writeTaskLocked(t); err != nil {
		return t, err
	}
	return t, nil
}

// Get retrieves a task by ID. Takes a shared flock so a concurrent
// Update (which truncates the file under an exclusive flock) cannot
// expose a partial or empty read.
func (s *TaskStore) Get(id string) (TeamTask, error) {
	path := filepath.Join(s.dir, id+".json")
	f, err := os.Open(path)
	if err != nil {
		return TeamTask{}, fmt.Errorf("get task %q: %w", id, err)
	}
	defer f.Close()

	if err := lockFileShared(f); err != nil {
		return TeamTask{}, fmt.Errorf("rlock task %q: %w", id, err)
	}
	defer unlockFile(f)

	data, err := io.ReadAll(f)
	if err != nil {
		return TeamTask{}, fmt.Errorf("read task %q: %w", id, err)
	}
	var t TeamTask
	if err := json.Unmarshal(data, &t); err != nil {
		return TeamTask{}, fmt.Errorf("parse task %q: %w", id, err)
	}
	return t, nil
}

// Update writes an updated task to disk atomically, holding an exclusive
// lock on the task file for the duration of the write.
func (s *TaskStore) Update(t TeamTask) error {
	return s.writeTask(t)
}

// Delete removes a task file.
func (s *TaskStore) Delete(id string) error {
	path := filepath.Join(s.dir, id+".json")
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("delete task %q: %w", id, err)
	}
	return nil
}

// List returns all tasks sorted by numeric ID.
func (s *TaskStore) List() ([]TeamTask, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	var tasks []TeamTask
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		name := e.Name()[:len(e.Name())-5]
		if name == ".store" { // skip the store lock file
			continue
		}
		t, err := s.Get(name)
		if err != nil {
			continue
		}
		tasks = append(tasks, t)
	}
	sort.Slice(tasks, func(i, j int) bool {
		a, _ := strconv.Atoi(tasks[i].ID)
		b, _ := strconv.Atoi(tasks[j].ID)
		return a < b
	})
	return tasks, nil
}

// writeTask holds an exclusive lock on the task file and rewrites it.
// Truncate+seek+write under flock, so concurrent Update calls on the same
// task serialize instead of racing.
func (s *TaskStore) writeTask(t TeamTask) error {
	path := filepath.Join(s.dir, t.ID+".json")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("open task %q: %w", t.ID, err)
	}
	defer f.Close()

	if err := lockFile(f); err != nil {
		return fmt.Errorf("lock task %q: %w", t.ID, err)
	}
	defer unlockFile(f)

	return s.writeTaskLockedFile(f, t)
}

// writeTaskLocked writes the task assuming the caller already holds the
// store-level lock (used by Create). It takes the per-task lock additionally
// so readers using Get+flock see a consistent file.
func (s *TaskStore) writeTaskLocked(t TeamTask) error {
	return s.writeTask(t)
}

func (s *TaskStore) writeTaskLockedFile(f *os.File, t TeamTask) error {
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal task: %w", err)
	}
	if err := f.Truncate(0); err != nil {
		return fmt.Errorf("truncate task: %w", err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		return fmt.Errorf("seek task: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write task: %w", err)
	}
	return nil
}

// nextIDLocked must be called with the store lock held.
func (s *TaskStore) nextIDLocked() (int, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return 1, nil
	}
	max := 0
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		name := e.Name()[:len(e.Name())-5]
		if n, err := strconv.Atoi(name); err == nil && n > max {
			max = n
		}
	}
	return max + 1, nil
}

// lockStore acquires an exclusive flock on a sentinel file inside the
// task dir and returns an unlock function. Callers must defer the unlock.
func (s *TaskStore) lockStore() (func(), error) {
	path := filepath.Join(s.dir, ".store.lock")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open task store lock: %w", err)
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("lock task store: %w", err)
	}
	return func() {
		unlockFile(f)
		f.Close()
	}, nil
}
