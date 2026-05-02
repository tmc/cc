// Command cmsg formats stdin as a single Claude message in NDJSON.
//
// cmsg reads stdin to EOF, optionally runs the content through a Go
// text/template, optionally attaches an image, and writes one
// newline-terminated JSON object describing a Claude message.
//
// # Usage
//
//	cmsg [-type TYPE] [-template TMPL] [-image PATH]
//
// # Flags
//
//	-type TYPE       Message type / role: user (default), assistant, system.
//	-template TMPL   Go text/template applied to stdin; the input is `.`.
//	-image PATH      Path to an image file. The bytes are base64-encoded
//	                 and attached as an `image` content block whose media
//	                 type is detected from the file contents.
//
// # Output
//
// One line of JSON shaped as:
//
//	{"type":"user","message":{"role":"user","content":[{"type":"text","text":"..."}]}}
//
// When -image is supplied, an additional `{"type":"image","source":{...}}`
// content block is included.
//
// # Examples
//
// Wrap stdin as a user message:
//
//	echo "hello, claude" | cmsg
//
// Wrap a file as an assistant message:
//
//	cat reply.txt | cmsg -type assistant
//
// Apply a template before wrapping:
//
//	cat code.go | cmsg -template "Review this code:\n\n{{.}}"
//
// Attach an image:
//
//	echo "what's in this?" | cmsg -image screenshot.png
package main
