// Package har parses HTTP Archive (HAR) files exported from Proxyman
// and extracts Anthropic API request/response data for ingestion into CASS.
//
// Proxyman exports individual request/response pairs as JSON files with the
// structure:
//
//	{
//	  "request": { "method", "url", "headers", "postData": { "text": "JSON" } },
//	  "response": { "status", "headers", "content": { "text": "base64", "encoding": "base64" } }
//	}
//
// The response body is a base64-encoded SSE stream. The parser decodes it and
// extracts the final token usage from the message_delta event, rate-limit
// headers from the response, and context composition from the request body.
//
// Session linkage uses the session ID embedded in the request body's
// metadata.user_id field (format: "user_..._session_<UUID>").
//
// # Usage
//
// Parse a single HAR file or scan a directory:
//
//	req, err := har.ParseFile("/path/to/export.har")
//	if err != nil {
//		log.Fatal(err)
//	}
//	fmt.Println(req.SessionID, req.Model, req.OutputTokens)
//
//	reqs, err := har.ScanDirContext(ctx, "/path/to/har/dir")
//	if err != nil {
//		log.Fatal(err)
//	}
//
// [ParseSessionV2File] / [ScanSessionV2Dir] handle Proxyman's newer
// .proxymansessionv2 / .proxymanlogv2 captures. [ScanArtifactDirsContext]
// scans ~/.it2/sessions/*/proxy-traffic.*.jsonl artifact streams.
package har
