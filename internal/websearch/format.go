package websearch

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// FormatForModel renders search results as a numbered plain-text block for the
// upstream model's tool_result content. On error it returns a short notice so
// the model can degrade gracefully instead of the request failing.
//
//	[1] Title — https://url (2026-07-01)
//	    snippet...
func FormatForModel(results []Result, searchErr error) string {
	if searchErr != nil {
		return "Web search is currently unavailable. Answer from your existing knowledge and note that live results could not be retrieved."
	}
	if len(results) == 0 {
		return "No results found for this query."
	}
	var sb strings.Builder
	for i, r := range results {
		writeNumbered(&sb, i+1, r.Title, r.URL, r.PublishedAt, r.Content)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// writeNumbered appends one numbered entry to sb in the shared model-facing
// format, used both by FormatForModel and by history normalization.
func writeNumbered(sb *strings.Builder, n int, title, url, date, snippet string) {
	fmt.Fprintf(sb, "[%d] %s — %s", n, strings.TrimSpace(title), strings.TrimSpace(url))
	if d := strings.TrimSpace(date); d != "" {
		fmt.Fprintf(sb, " (%s)", d)
	}
	sb.WriteString("\n")
	if s := strings.TrimSpace(snippet); s != "" {
		sb.WriteString("    ")
		sb.WriteString(s)
		sb.WriteString("\n")
	}
}

// ServerToolUseBlock builds the client-facing server_tool_use block mirroring
// the model's web_search call. id is the server-tool id (srvtoolu_ prefixed);
// query is the searched query.
func ServerToolUseBlock(id, query string) ContentBlock {
	input, _ := json.Marshal(WebSearchInput{Query: query})
	return ContentBlock{
		Type:  "server_tool_use",
		ID:    id,
		Name:  ClientToolName,
		Input: input,
	}
}

// webSearchResultItem is one entry in a web_search_tool_result content array.
// encrypted_content holds base64(snippet) as a placeholder: Anthropic uses it
// for citation decryption, which the gateway doesn't implement, but Claude Code
// only renders title/url. On the next turn NormalizeHistory decodes it back.
type webSearchResultItem struct {
	Type             string `json:"type"`
	Title            string `json:"title"`
	URL              string `json:"url"`
	EncryptedContent string `json:"encrypted_content"`
	PageAge          string `json:"page_age,omitempty"`
}

// webSearchResultError is the content shape when a search failed.
type webSearchResultError struct {
	Type      string `json:"type"`
	ErrorCode string `json:"error_code"`
}

// ErrorBlock builds a client-facing web_search_tool_result carrying an error
// with the given Anthropic error_code (e.g. "unavailable", "max_uses_exceeded").
func ErrorBlock(toolUseID, errorCode string) ContentBlock {
	content, _ := json.Marshal(webSearchResultError{
		Type:      "web_search_tool_result_error",
		ErrorCode: errorCode,
	})
	return ContentBlock{
		Type:      "web_search_tool_result",
		ToolUseID: toolUseID,
		Content:   content,
	}
}

// WebSearchResultBlock builds the client-facing web_search_tool_result block. On
// error it emits a web_search_tool_result_error content (error_code
// "unavailable"); otherwise a list of web_search_result items with base64
// snippet placeholders.
func WebSearchResultBlock(toolUseID string, results []Result, searchErr error) ContentBlock {
	if searchErr != nil {
		return ErrorBlock(toolUseID, "unavailable")
	}
	items := make([]webSearchResultItem, 0, len(results))
	for _, r := range results {
		items = append(items, webSearchResultItem{
			Type:             "web_search_result",
			Title:            r.Title,
			URL:              r.URL,
			EncryptedContent: base64.StdEncoding.EncodeToString([]byte(r.Content)),
			PageAge:          r.PublishedAt,
		})
	}
	content, _ := json.Marshal(items)
	return ContentBlock{
		Type:      "web_search_tool_result",
		ToolUseID: toolUseID,
		Content:   content,
	}
}
