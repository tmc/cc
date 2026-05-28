// Package ccfs provides atomic-write and advisory-lock helpers shared across
// the cc storage packages.
//
// # Atomic writes
//
// [WriteFileAtomic] writes to a temp file in the same directory and renames it
// into place, preserving an existing file's mode:
//
//	if err := ccfs.WriteFileAtomic(path, data, 0o644); err != nil {
//		log.Fatal(err)
//	}
//
// # Locking
//
// [LockFile], [LockFileShared], and [UnlockFile] wrap flock(2) for serializing
// concurrent readers and writers of a file:
//
//	f, _ := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
//	defer f.Close()
//	if err := ccfs.LockFile(f); err != nil {
//		log.Fatal(err)
//	}
//	defer ccfs.UnlockFile(f)
package ccfs
