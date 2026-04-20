package cc

import (
	"fmt"
	"sync"
	"testing"
)

// TestTaskStoreConcurrentCreate asserts that concurrent Create calls each
// get a unique ID. Without the store-level lock two callers would race on
// nextID/writeTask and pick duplicate IDs.
func TestTaskStoreConcurrentCreate(t *testing.T) {
	t.Setenv("CC_TASKS_DIR", t.TempDir())
	s, err := NewTaskStore("team")
	if err != nil {
		t.Fatal(err)
	}

	const n = 50
	ids := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			task, err := s.Create(TeamTask{Subject: fmt.Sprintf("t%d", i)})
			if err != nil {
				t.Errorf("create %d: %v", i, err)
				return
			}
			ids[i] = task.ID
		}(i)
	}
	wg.Wait()

	seen := make(map[string]bool, n)
	for i, id := range ids {
		if id == "" {
			t.Fatalf("task %d: empty id", i)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q from task %d", id, i)
		}
		seen[id] = true
	}

	tasks, err := s.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tasks) != n {
		t.Fatalf("list returned %d tasks, want %d", len(tasks), n)
	}
}

// TestTaskStoreConcurrentUpdate asserts that concurrent Update calls on
// the same task serialize without corrupting the file. Without per-task
// flock a concurrent reader could observe a truncated file.
func TestTaskStoreConcurrentUpdate(t *testing.T) {
	t.Setenv("CC_TASKS_DIR", t.TempDir())
	s, err := NewTaskStore("team")
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.Create(TeamTask{Subject: "test"})
	if err != nil {
		t.Fatal(err)
	}

	const iters = 200
	var wg sync.WaitGroup
	for i := range iters {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			t := task
			t.Description = fmt.Sprintf("update %d", i)
			if err := s.Update(t); err != nil {
				return
			}
		}(i)
	}
	wg.Wait()

	got, err := s.Get(task.ID)
	if err != nil {
		t.Fatalf("get: %v (file may be corrupted)", err)
	}
	if got.Subject != "test" {
		t.Errorf("subject lost: got %q, want %q", got.Subject, "test")
	}
	if got.ID != task.ID {
		t.Errorf("id changed: got %q, want %q", got.ID, task.ID)
	}
}
