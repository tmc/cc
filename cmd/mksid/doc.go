// Command mksid prints a timestamp-sorted session ID with git context.
//
// The default output is `YYYYMMDD-HHMMSS-UUID8-GITHASH8`, where the
// UUID component is the first 8 hex chars of a fresh UUID v4 and the
// git hash is the first 8 hex chars of SHA-256(<git common dir>) for
// the current working directory. Outside a git repository the CWD
// path is hashed instead; with -no-git the hash slot is `00000000`.
//
// # Usage
//
//	mksid [flags]
//
// # Flags
//
//	-format FMT    Output format: default, uuid-only, timestamp-only, json.
//	-no-git        Skip git detection; emit "00000000" for the hash slot.
//	-verbose       Log git detection details to stderr.
//
// In json mode, the printed object has fields `id`, `timestamp`,
// `uuid`, and `git_hash`.
//
// # Examples
//
// Generate a session ID:
//
//	mksid
//
// Use it to name a per-session NDJSON file:
//
//	SID=$(mksid)
//	echo "hello" | cmsg > session-$SID.ndjson
//
// Emit JSON for scripting:
//
//	mksid -format json | jq -r .git_hash
package main
