package websearch

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
)

// SplitResponse partitions an upstream response's content blocks into the
// web_search tool_use calls the gateway must resolve, any other client tool_use
// calls the caller must handle, and the remaining blocks (text, thinking, ...)
// that pass through to the client. Order within each group is preserved.
func SplitResponse(blocks []ContentBlock) (webCalls, clientCalls, others []ContentBlock) {
	for _, b := range blocks {
		if b.Type == "tool_use" && b.Name == ClientToolName {
			webCalls = append(webCalls, b)
		} else if b.Type == "tool_use" {
			clientCalls = append(clientCalls, b)
		} else {
			others = append(others, b)
		}
	}
	return
}

// QueryOf extracts the query string from a web_search tool_use block's input.
func QueryOf(b ContentBlock) string {
	var in WebSearchInput
	_ = json.Unmarshal(b.Input, &in)
	return in.Query
}

// NewServerToolID returns a fresh server-tool id with Anthropic's srvtoolu_
// prefix, used for the client-facing server_tool_use / web_search_tool_result
// pairing.
func NewServerToolID() string {
	return "srvtoolu_" + randHex(12)
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "0000000000000000"[:2*n]
	}
	return hex.EncodeToString(b)
}

// MaxUsesExceededResult builds the tool_result content fed to the model when the
// per-request search cap is hit: the model finishes from what it already has.
func MaxUsesExceededResult() string {
	return "The maximum number of web searches for this request has been reached. Answer using the results already gathered."
}
