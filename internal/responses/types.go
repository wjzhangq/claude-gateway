// Package responses adapts the OpenAI Responses API (/v1/responses, used by the
// Codex CLI) to the Anthropic Messages protocol the backend channel already
// speaks. It converts an inbound Responses request into a
// websearch.MessagesRequest (the gateway's Anthropic pivot type), and converts
// the assembled Anthropic content blocks back into Responses output items —
// both as a single JSON object and as a synthesized Responses SSE stream.
//
// The adapter is intentionally stateless: previous_response_id is rejected and
// store is always reported false, so the gateway keeps no per-response state and
// the client must send full conversation history in input[] each turn.
package responses

import "encoding/json"

// Request is an inbound /v1/responses request. Only the fields the adapter
// manipulates are typed; the polymorphic input list is decoded item-by-item.
type Request struct {
	Model              string          `json:"model"`
	Instructions       string          `json:"instructions,omitempty"`
	Input              []InputItem     `json:"input"`
	Tools              []Tool          `json:"tools,omitempty"`
	ToolChoice         json.RawMessage `json:"tool_choice,omitempty"`
	Stream             bool            `json:"stream,omitempty"`
	Store              *bool           `json:"store,omitempty"`
	PreviousResponseID string          `json:"previous_response_id,omitempty"`
	MaxOutputTokens    *int            `json:"max_output_tokens,omitempty"`
	Temperature        *float64        `json:"temperature,omitempty"`
	TopP               *float64        `json:"top_p,omitempty"`
}

// InputItem is one entry in the Responses input[] array. It is polymorphic on
// Type; the fields used depend on the type:
//   - "message": Role + Content (string or content-part array)
//   - "function_call": CallID, Name, Arguments (arguments is a JSON string)
//   - "function_call_output": CallID, Output
//   - "web_search_call": dropped on the way in (info already in the text)
//   - "reasoning": dropped in v1
//
// A bare string input (the whole request `input` given as a string) is
// normalized during decode into a single message item with role "user".
type InputItem struct {
	Type      string          `json:"type"`
	Role      string          `json:"role,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments string          `json:"arguments,omitempty"`
	Output    json.RawMessage `json:"output,omitempty"`
	ID        string          `json:"id,omitempty"`
	Status    string          `json:"status,omitempty"`
}

// Tool is one entry in the Responses tools[] array. Covers the server web_search
// tool ("web_search" / "web_search_preview") and client function tools
// ("function", with a JSON-schema Parameters object).
type Tool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// ContentPart is one part of a message item's content array (input_text /
// output_text). A message's content may also be a bare string.
type ContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Response is the top-level /v1/responses object returned to the client.
type Response struct {
	ID                string             `json:"id"`
	Object            string             `json:"object"`
	CreatedAt         int64              `json:"created_at"`
	Model             string             `json:"model"`
	Status            string             `json:"status"`
	Output            []OutputItem       `json:"output"`
	Usage             *Usage             `json:"usage,omitempty"`
	Store             bool               `json:"store"`
	IncompleteDetails *IncompleteDetails `json:"incomplete_details,omitempty"`
	Error             *RespError         `json:"error,omitempty"`
}

// OutputItem is one item in the response output[] array: an assistant message,
// a web_search_call, or a function_call.
type OutputItem struct {
	Type    string              `json:"type"`
	ID      string              `json:"id,omitempty"`
	Status  string              `json:"status,omitempty"`
	Role    string              `json:"role,omitempty"`
	Content []OutputContentPart `json:"content,omitempty"`
	CallID  string              `json:"call_id,omitempty"`
	Name    string              `json:"name,omitempty"`
	// Arguments is a JSON-encoded string of the function arguments.
	Arguments string           `json:"arguments,omitempty"`
	Action    *WebSearchAction `json:"action,omitempty"`
}

// OutputContentPart is one part of an assistant message item's content array.
type OutputContentPart struct {
	Type        string       `json:"type"`
	Text        string       `json:"text"`
	Annotations []Annotation `json:"annotations"`
}

// Annotation is a url_citation entry on an output_text part. Populated in a
// later milestone; kept as an always-empty slice for now so the wire shape is
// correct.
type Annotation struct {
	Type       string `json:"type"`
	URL        string `json:"url,omitempty"`
	Title      string `json:"title,omitempty"`
	StartIndex int    `json:"start_index"`
	EndIndex   int    `json:"end_index"`
}

// WebSearchAction is the action of a web_search_call output item.
type WebSearchAction struct {
	Type  string `json:"type"`
	Query string `json:"query"`
}

// Usage is the Responses usage block.
type Usage struct {
	InputTokens         int                 `json:"input_tokens"`
	OutputTokens        int                 `json:"output_tokens"`
	TotalTokens         int                 `json:"total_tokens"`
	OutputTokensDetails OutputTokensDetails `json:"output_tokens_details"`
}

// OutputTokensDetails carries the reasoning-token breakdown (always 0 here).
type OutputTokensDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

// IncompleteDetails explains a non-completed status (e.g. max_output_tokens).
type IncompleteDetails struct {
	Reason string `json:"reason"`
}

// RespError is a Responses-shaped error object.
type RespError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}
