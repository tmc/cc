// Command cmsg formats stdin as Claude messages in NDJSON format.
//
// # Usage
//
//	cmsg [-type TYPE]
//
// The cmsg command reads from stdin and outputs a Claude message as
// newline-delimited JSON (NDJSON). This format is streaming-compatible
// and designed for use in Unix pipelines.
//
// # Options
//
//	-type string
//	    Message type (default "user")
//	    Valid types: user, assistant, system
//
// # Message Format
//
// Output is a single-line JSON object representing a Claude message:
//
//	{"type":"user","content":"the input text"}
//
// The entire stdin is read and placed into the content field. For
// streaming scenarios, call cmsg once per logical message.
//
// # Streaming Compatibility
//
// The cmsg command is designed for streaming workflows:
//
//   - Reads from stdin until EOF
//   - Outputs single-line NDJSON
//   - No buffering beyond stdin read
//   - Composable via pipes
//
// # Examples
//
// Format a simple user message:
//
//	echo "Hello, Claude" | cmsg
//
// Create a session with multiple messages:
//
//	SID=$(mksid)
//	echo "First message" | cmsg | tee -a session-$SID.ndjson
//	echo "Second message" | cmsg | tee -a session-$SID.ndjson
//
// Format an assistant message:
//
//	echo "Response text" | cmsg -type assistant
//
// Pipe multiple sources:
//
//	cat prompt.txt | cmsg | tee session.ndjson
//	cat response.txt | cmsg -type assistant | tee -a session.ndjson
//
// # Integration
//
// The cmsg command integrates with other cc utilities:
//
//   - Use mksid to generate session IDs
//   - Append output to session files for replay with creplay
//   - Chain with other tools via standard Unix pipes
package main
