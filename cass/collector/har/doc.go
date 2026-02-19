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
package har
