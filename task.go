package cc

import (
	"encoding/json"
	"fmt"
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
func NewTaskStore(namespace string) (*TaskStore, error) {
	base := os.Getenv("CC_TASKS_DIR")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("task store: %w", err)
		}
		base = filepath.Join(home, ".claude", "tasks")
	}
	dir := filepath.Join(base, namespace)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create task dir: %w", err)
	}
	return &TaskStore{dir: dir}, nil
}

// Create creates a new task with an auto-incremented ID.
func (s *TaskStore) Create(t TeamTask) (TeamTask, error) {
	id, err := s.nextID()
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
	if err := s.writeTask(t); err != nil {
		return t, err
	}
	return t, nil
}

// Get retrieves a task by ID.
func (s *TaskStore) Get(id string) (TeamTask, error) {
	path := filepath.Join(s.dir, id+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return TeamTask{}, fmt.Errorf("get task %q: %w", id, err)
	}
	var t TeamTask
	if err := json.Unmarshal(data, &t); err != nil {
		return TeamTask{}, fmt.Errorf("parse task %q: %w", id, err)
	}
	return t, nil
}

// Update writes an updated task to disk.
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
		id := e.Name()[:len(e.Name())-5] // strip .json
		t, err := s.Get(id)
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

func (s *TaskStore) writeTask(t TeamTask) error {
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal task: %w", err)
	}
	path := filepath.Join(s.dir, t.ID+".json")
	return os.WriteFile(path, data, 0o644)
}

func (s *TaskStore) nextID() (int, error) {
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
