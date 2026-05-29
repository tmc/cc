// Package cctaskstore is a flock-serialized file store for team tasks under
// ~/.claude/tasks/<namespace>/.
//
// A [TaskStore] holds [TeamTask] records as one JSON file per task.
// [TaskStore.Create] allocates an auto-incrementing ID under a store-level lock;
// reads and writes take per-task flocks so concurrent callers never see a
// partial file. The CC_TASKS_DIR environment variable overrides the base
// directory.
//
//	store, err := cctaskstore.NewTaskStore(ctx, "review")
//	if err != nil {
//		log.Fatal(err)
//	}
//	t, err := store.Create(cctaskstore.TeamTask{Subject: "ship it"})
package cctaskstore
