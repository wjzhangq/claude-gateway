// Package websearch simulates Anthropic's web_search server tool at the gateway
// layer for the backend channel. Backend upstreams are third-party Anthropic
// compatible relays that don't implement the server tool, so the gateway
// rewrites it into a plain client tool, runs an agent loop that resolves the
// model's web_search calls against SearXNG, and finally emits native
// server_tool_use + web_search_tool_result blocks the client renders directly.
//
// This file defines the subset of the Anthropic Messages protocol the loop
// needs. It deliberately keeps unknown top-level request fields as raw JSON so
// the request can be forwarded upstream losslessly (system, thinking,
// tool_choice, metadata, ... must survive the round trip).
package websearch

import "encoding/json"

// ServerToolPrefix is the type prefix Anthropic uses for the web_search server
// tool (e.g. "web_search_20250305"). Any tool whose type carries this prefix is
// treated as a web_search server tool regardless of the version suffix.
const ServerToolPrefix = "web_search_"

// ClientToolName is the plain client tool name the server tool is rewritten to
// before the request is forwarded upstream. The model calls it like any other
// tool; the gateway intercepts the call.
const ClientToolName = "web_search"

// MessagesRequest is an Anthropic /v1/messages request. Known fields the loop
// manipulates are typed; everything else is preserved in Extra so the forwarded
// body keeps fields like system / thinking / tool_choice / metadata intact.
type MessagesRequest struct {
	Model    string            `json:"-"`
	Messages []Message         `json:"-"`
	Tools    []json.RawMessage `json:"-"`
	Stream   bool              `json:"-"`

	// Extra holds every other top-level field verbatim (system, thinking,
	// tool_choice, max_tokens, metadata, ...). model/messages/tools/stream are
	// removed from Extra during decode and re-serialized from the typed fields.
	Extra map[string]json.RawMessage `json:"-"`
}

// Message is one conversation turn. Content is kept as blocks; a bare-string
// content is normalized to a single text block on decode.
type Message struct {
	Role    string
	Blocks  []ContentBlock
	rawText string // set when the original content was a bare string
	wasText bool
}

// ContentBlock covers the block types the loop reads or emits: text, tool_use,
// tool_result, server_tool_use, web_search_tool_result. Only fields that are
// actually used are typed; unknown fields on a passthrough block are preserved
// via Raw when the block is not one the loop rewrites.
type ContentBlock struct {
	Type string `json:"type"`

	// text
	Text string `json:"text,omitempty"`

	// tool_use / server_tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// tool_result / web_search_tool_result
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`

	// Raw preserves the original bytes for blocks the loop passes through
	// unchanged (e.g. thinking). When set, it is emitted verbatim.
	Raw json.RawMessage `json:"-"`
}

// WebSearchInput is the argument shape of a web_search client tool call.
type WebSearchInput struct {
	Query string `json:"query"`
}

// MessagesResponse is the subset of an upstream /v1/messages response the loop
// consumes and the shape it emits to the client.
type MessagesResponse struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Role       string         `json:"role"`
	Model      string         `json:"model"`
	Content    []ContentBlock `json:"content"`
	StopReason string         `json:"stop_reason"`
	Usage      Usage          `json:"usage"`
}

// Usage aggregates token counts across the loop's upstream rounds and tracks the
// number of gateway-executed web searches (server_tool_use.web_search_requests),
// matching Anthropic's usage shape.
type Usage struct {
	InputTokens              int            `json:"input_tokens"`
	OutputTokens             int            `json:"output_tokens"`
	CacheCreationInputTokens int            `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int            `json:"cache_read_input_tokens,omitempty"`
	ServerToolUse            *ServerToolUse `json:"server_tool_use,omitempty"`
}

// ServerToolUse counts server-side tool invocations for the usage block.
type ServerToolUse struct {
	WebSearchRequests int `json:"web_search_requests"`
}

// Add accumulates another round's token usage into u. Cache and server-tool
// counters are summed too so the final response reflects the whole loop.
func (u *Usage) Add(o Usage) {
	u.InputTokens += o.InputTokens
	u.OutputTokens += o.OutputTokens
	u.CacheCreationInputTokens += o.CacheCreationInputTokens
	u.CacheReadInputTokens += o.CacheReadInputTokens
	if o.ServerToolUse != nil {
		if u.ServerToolUse == nil {
			u.ServerToolUse = &ServerToolUse{}
		}
		u.ServerToolUse.WebSearchRequests += o.ServerToolUse.WebSearchRequests
	}
}

// Result is one normalized SearXNG search result.
type Result struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Content     string `json:"content"`
	PublishedAt string `json:"published_date,omitempty"`
}
