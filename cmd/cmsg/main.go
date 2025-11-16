package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"text/template"
)

func main() {
	var msgType string
	var tmplStr string
	var imageFile string
	flag.StringVar(&msgType, "type", "user", "Message type (user, assistant, system)")
	flag.StringVar(&tmplStr, "template", "", "Go template for content transformation")
	flag.StringVar(&imageFile, "image", "", "Path to image file to include")
	flag.Parse()

	// Validate message type
	switch msgType {
	case "user", "assistant", "system":
		// valid
	default:
		fmt.Fprintf(os.Stderr, "invalid message type: %q (must be user, assistant, or system)\n", msgType)
		os.Exit(1)
	}

	// Read all of stdin
	content, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading stdin: %v\n", err)
		os.Exit(1)
	}

	// Apply template if provided
	text := string(content)
	if tmplStr != "" {
		tmpl, err := template.New("content").Parse(tmplStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error parsing template: %v\n", err)
			os.Exit(1)
		}
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, text); err != nil {
			fmt.Fprintf(os.Stderr, "error executing template: %v\n", err)
			os.Exit(1)
		}
		text = buf.String()
	}

	// Create content blocks
	type ContentBlock struct {
		Type      string `json:"type"`
		Text      string `json:"text,omitempty"`
		Source    *struct {
			Type      string `json:"type"`
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
		} `json:"source,omitempty"`
	}

	var contentBlocks []ContentBlock

	// Add text content if present
	if text != "" {
		contentBlocks = append(contentBlocks, ContentBlock{
			Type: "text",
			Text: text,
		})
	}

	// Add image content if provided
	if imageFile != "" {
		imageData, err := os.ReadFile(imageFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error reading image file: %v\n", err)
			os.Exit(1)
		}

		// Detect media type
		mediaType := http.DetectContentType(imageData)

		// Encode to base64
		b64Data := base64.StdEncoding.EncodeToString(imageData)

		contentBlocks = append(contentBlocks, ContentBlock{
			Type: "image",
			Source: &struct {
				Type      string `json:"type"`
				MediaType string `json:"media_type"`
				Data      string `json:"data"`
			}{
				Type:      "base64",
				MediaType: mediaType,
				Data:      b64Data,
			},
		})
	}

	// Create the message structure matching the expected format
	msg := struct {
		Type    string `json:"type"`
		Message struct {
			Role    string         `json:"role"`
			Content []ContentBlock `json:"content"`
		} `json:"message"`
	}{
		Type: msgType,
	}

	msg.Message.Role = msgType
	msg.Message.Content = contentBlocks

	// Output as NDJSON (compact JSON with newline)
	data, err := json.Marshal(msg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error encoding json: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(data))
}
