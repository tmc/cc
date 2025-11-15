// Command mksid generates timestamp-sorted session IDs with git context.
//
// # Usage
//
//	mksid [options]
//
// The mksid command generates unique session identifiers that are:
//
//   - Timestamp-sorted for chronological ordering
//   - Enriched with git repository context
//   - Designed for fork tracking capability
//   - Compatible with UUID-based tooling
//
// # Output Format
//
// The generated session ID has the following structure:
//
//	YYYYMMDD-HHMMSS-UUID-GITHASH
//
// Components:
//
//	YYYYMMDD-HHMMSS  Timestamp prefix for sorting
//	UUID             Unique identifier component
//	GITHASH          Hash of git repo path (first 8 chars of SHA-256)
//
// This format ensures:
//
//   - Lexicographic sorting matches chronological order
//   - Global uniqueness via UUID component
//   - Repository correlation via git hash
//   - Reserved structure for future fork tracking
//
// # Git Repository Context
//
// The git hash component is computed from:
//
//   - Current working directory (CWD)
//   - Path traversal to find nearest .git directory
//   - SHA-256 hash of the git repository root path
//   - First 8 characters of the hash for brevity
//
// If no git repository is found:
//
//   - Falls back to CWD path hash
//   - Warns on stderr (does not fail)
//   - Continues with generated ID
//
// # Session ID Properties
//
// Generated IDs have these properties:
//
//   - Sortable: IDs sort chronologically by creation time
//   - Unique: UUID component ensures global uniqueness
//   - Contextual: Git hash links to repository context
//   - Stable: Same repo location produces same hash component
//   - Extensible: Format reserves space for fork metadata
//
// # Options
//
//	-format string
//	    Output format (default "default")
//	    Formats: default, uuid-only, timestamp-only, json
//
//	-no-git
//	    Disable git repository detection
//	    Skip git hash component in output
//
//	-verbose
//	    Print verbose information to stderr
//	    Shows git repo path, hash computation, etc.
//
// # Examples
//
// Generate a session ID:
//
//	mksid
//	# Output: 20241114-153045-a1b2c3d4-e5f6g7h8
//
// Use in a script:
//
//	SID=$(mksid)
//	echo "Session: $SID"
//	echo "user message" | cmsg > session-$SID.ndjson
//
// Generate UUID only:
//
//	mksid -format uuid-only
//	# Output: a1b2c3d4-e5f6-g7h8-i9j0-k1l2m3n4o5p6
//
// Generate with verbose output:
//
//	mksid -verbose
//	# stderr: Git repo: /path/to/repo
//	# stderr: Repo hash: e5f6g7h8...
//	# stdout: 20241114-153045-a1b2c3d4-e5f6g7h8
//
// Skip git context:
//
//	mksid -no-git
//	# Output: 20241114-153045-a1b2c3d4-00000000
//
// JSON output for scripting:
//
//	mksid -format json
//	# Output: {"id":"...","timestamp":"...","git_hash":"..."}
//
// # Fork Tracking
//
// The session ID format reserves space for fork tracking:
//
//   - Future: Support for session fork relationships
//   - Planned: from/to session ID references
//   - Use case: Branching conversation threads
//   - Design: Backward compatible with current format
//
// # Integration
//
// The mksid command integrates with other cc utilities:
//
//   - Session IDs used by cmsg for message correlation
//   - Session IDs used by creplay for session lookup
//   - Git context enables cross-repository session tracking
//   - Timestamp sorting enables chronological session ordering
//
// # Error Handling
//
// The command handles various conditions:
//
//   - Missing git repository: warns but continues
//   - Permission errors: falls back to CWD hash
//   - Invalid paths: uses safe defaults
//   - Always produces valid output (fails only on stdout errors)
package main
