package websearch

import (
	"encoding/base64"
	"encoding/json"
	"strings"
)

// clientToolSchema is the plain client tool injected in place of the web_search
// server tool before the request is forwarded upstream.
var clientToolSchema = json.RawMessage(`{
  "name": "web_search",
  "description": "Search the web for current information. Provide a single 'query' string. Returns a numbered list of results with title, url, snippet and date.",
  "input_schema": {
    "type": "object",
    "properties": {"query": {"type": "string", "description": "The search query"}},
    "required": ["query"]
  }
}`)

// serverTool describes a detected web_search server tool from the request.
type serverTool struct {
	Type          string   `json:"type"`
	Name          string   `json:"name"`
	MaxUses       *int     `json:"max_uses"`
	AllowedDomain []string `json:"allowed_domains"`
	BlockedDomain []string `json:"blocked_domains"`
}

// DetectWebSearchTool scans a tools array for an Anthropic web_search server
// tool (type prefixed with "web_search_"). It returns whether one was found
// plus its max_uses (-1 when unspecified) and domain filters.
func DetectWebSearchTool(tools []json.RawMessage) (found bool, maxUses int, allowed, blocked []string) {
	maxUses = -1
	for _, raw := range tools {
		var t serverTool
		if err := json.Unmarshal(raw, &t); err != nil {
			continue
		}
		if strings.HasPrefix(t.Type, ServerToolPrefix) {
			found = true
			if t.MaxUses != nil {
				maxUses = *t.MaxUses
			}
			allowed = t.AllowedDomain
			blocked = t.BlockedDomain
			return
		}
	}
	return
}

// DetectInBody is a convenience wrapper that detects the web_search server tool
// directly from a raw request body, without a full decode. Used as the cheap
// gate on the proxy hot path.
func DetectInBody(body []byte) bool {
	var top struct {
		Tools []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(body, &top); err != nil {
		return false
	}
	found, _, _, _ := DetectWebSearchTool(top.Tools)
	return found
}

// RewriteTools removes the web_search server tool from the request's tools and
// appends the plain client tool the model will call. Other tools (client tools
// the caller defined) are preserved.
func (r *MessagesRequest) RewriteTools() {
	kept := make([]json.RawMessage, 0, len(r.Tools))
	for _, raw := range r.Tools {
		var t serverTool
		if err := json.Unmarshal(raw, &t); err == nil && strings.HasPrefix(t.Type, ServerToolPrefix) {
			continue // drop the server tool
		}
		kept = append(kept, raw)
	}
	kept = append(kept, clientToolSchema)
	r.Tools = kept
}

// NormalizeHistory rewrites prior assistant turns that carry gateway-emitted
// server_tool_use + web_search_tool_result blocks back into the standard
// tool_use / tool_result shape the upstream understands. Without this, a
// multi-turn conversation would replay blocks the backend relay can't parse.
//
// For each server_tool_use block it emits a standard assistant tool_use block
// (name=web_search); the paired web_search_tool_result (carried in the SAME
// assistant turn by Anthropic's format) becomes a separate following user turn
// with a tool_result block whose text is decoded from the result's
// encrypted_content placeholders. Plain text blocks are preserved in order.
func (r *MessagesRequest) NormalizeHistory() {
	out := make([]Message, 0, len(r.Messages))
	for _, msg := range r.Messages {
		if msg.Role != "assistant" || !containsServerBlocks(msg.Blocks) {
			out = append(out, msg)
			continue
		}
		assistantBlocks := make([]ContentBlock, 0, len(msg.Blocks))
		var pendingResults []ContentBlock
		for _, b := range msg.Blocks {
			switch b.Type {
			case "server_tool_use":
				assistantBlocks = append(assistantBlocks, ContentBlock{
					Type:  "tool_use",
					ID:    b.ID,
					Name:  ClientToolName,
					Input: b.Input,
				})
			case "web_search_tool_result":
				pendingResults = append(pendingResults, ContentBlock{
					Type:      "tool_result",
					ToolUseID: b.ToolUseID,
					Content:   decodeResultContent(b.Content),
				})
			default:
				assistantBlocks = append(assistantBlocks, b)
			}
		}
		out = append(out, Message{Role: "assistant", Blocks: assistantBlocks})
		if len(pendingResults) > 0 {
			out = append(out, Message{Role: "user", Blocks: pendingResults})
		}
	}
	r.Messages = out
}

// containsServerBlocks reports whether any block is a gateway server block.
func containsServerBlocks(blocks []ContentBlock) bool {
	for _, b := range blocks {
		if b.Type == "server_tool_use" || b.Type == "web_search_tool_result" {
			return true
		}
	}
	return false
}

// decodeResultContent turns a web_search_tool_result content array (each item a
// web_search_result with a base64 encrypted_content placeholder) back into a
// plain-text tool_result content the model can read. Falls back to the raw
// bytes when the shape is unexpected.
func decodeResultContent(content json.RawMessage) json.RawMessage {
	var items []struct {
		Title            string `json:"title"`
		URL              string `json:"url"`
		EncryptedContent string `json:"encrypted_content"`
		PageAge          string `json:"page_age"`
	}
	if err := json.Unmarshal(content, &items); err != nil || len(items) == 0 {
		return content
	}
	var sb strings.Builder
	for i, it := range items {
		snippet := ""
		if dec, err := base64.StdEncoding.DecodeString(it.EncryptedContent); err == nil {
			snippet = string(dec)
		}
		writeNumbered(&sb, i+1, it.Title, it.URL, it.PageAge, snippet)
	}
	b, _ := json.Marshal(sb.String())
	return b
}
