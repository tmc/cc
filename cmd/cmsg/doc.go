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
//	-template string
//	    Go template for content transformation
//	    Template receives stdin content as string data ({{.}})
//
//	-image string
//	    Path to image file to include in message
//	    Image is base64-encoded with auto-detected media type
//
// # Message Format
//
// Output is a single-line JSON object representing a Claude message:
//
//	{"type":"user","message":{"role":"user","content":[{"type":"text","text":"the input text"}]}}
//
// The entire stdin is read and placed into the text field of a content block.
// For streaming scenarios, call cmsg once per logical message.
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
// Use templates to transform content:
//
//	echo "code review" | cmsg -template "Please review: {{.}}"
//	cat file.go | cmsg -template "Analyze this code:\n\n{{.}}"
//
// Include images in messages:
//
//	echo "What's in this image?" | cmsg -image screenshot.png
//	cmsg -image diagram.jpg -template "Explain this diagram"
//
// # Integration
//
// The cmsg command integrates with other cc utilities:
//
//   - Use mksid to generate session IDs
//   - Append output to session files for replay with creplay
//   - Chain with other tools via standard Unix pipes
package main
