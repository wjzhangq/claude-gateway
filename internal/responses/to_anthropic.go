package responses

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/wjzhangq/claude-gateway/internal/websearch"
)

// ErrPreviousResponseID is returned when a request carries previous_response_id.
// The gateway is stateless (see package doc): the client must send full history.
var ErrPreviousResponseID = errors.New("previous_response_id is not supported; send the full conversation in input[]")

const defaultMaxTokens = 4096

// ToMessagesRequest converts an inbound Responses request into the gateway's
// Anthropic pivot (websearch.MessagesRequest). It returns the request plus
// whether a web_search server tool was requested, so the caller can decide
// whether to run the search loop's tool rewrite. Stateless-mode rejection
// (previous_response_id) happens here.
//
// Mapping summary:
//   - instructions + any system/developer-role input → Anthropic top-level system
//   - message items → user/assistant turns with text blocks
//   - function_call items → assistant tool_use blocks
//   - function_call_output items → user tool_result blocks
//   - web_search_call / reasoning items → dropped
//   - function tools → Anthropic tools with input_schema
//   - web_search tool → Anthropic web_search server tool (rewritten by the loop)
func ToMessagesRequest(req *Request) (*websearch.MessagesRequest, bool, error) {
	if strings.TrimSpace(req.PreviousResponseID) != "" {
		return nil, false, ErrPreviousResponseID
	}

	out := &websearch.MessagesRequest{
		Model:  req.Model,
		Stream: req.Stream,
		Extra:  map[string]json.RawMessage{},
	}

	// System: instructions first, then any system/developer-role messages.
	var systemParts []string
	if s := strings.TrimSpace(req.Instructions); s != "" {
		systemParts = append(systemParts, s)
	}

	// Build messages, coalescing consecutive same-role turns so the upstream
	// sees the alternating shape Anthropic expects.
	var msgs []websearch.Message
	appendBlocks := func(role string, blocks ...websearch.ContentBlock) {
		if len(blocks) == 0 {
			return
		}
		if n := len(msgs); n > 0 && msgs[n-1].Role == role {
			msgs[n-1].Blocks = append(msgs[n-1].Blocks, blocks...)
			return
		}
		msgs = append(msgs, websearch.Message{Role: role, Blocks: append([]websearch.ContentBlock(nil), blocks...)})
	}

	for _, it := range req.Input {
		switch it.Type {
		case "message":
			role := it.Role
			if role == "system" || role == "developer" {
				if t := strings.TrimSpace(it.contentText()); t != "" {
					systemParts = append(systemParts, t)
				}
				continue
			}
			if role == "" {
				role = "user"
			}
			text := it.contentText()
			if text != "" {
				appendBlocks(role, websearch.ContentBlock{Type: "text", Text: text})
			}
		case "function_call":
			appendBlocks("assistant", websearch.ContentBlock{
				Type:  "tool_use",
				ID:    it.CallID,
				Name:  it.Name,
				Input: argumentsToInput(it.Arguments),
			})
		case "function_call_output":
			appendBlocks("user", websearch.ContentBlock{
				Type:      "tool_result",
				ToolUseID: it.CallID,
				Content:   outputToContent(it.Output),
			})
		case "web_search_call", "reasoning":
			// Dropped: web_search_call info already lives in the following
			// assistant text; reasoning is not round-tripped in v1.
			continue
		default:
			continue
		}
	}
	out.Messages = msgs

	if len(systemParts) > 0 {
		out.Extra["system"] = mustJSONRaw(strings.Join(systemParts, "\n\n"))
	}

	// Tools.
	hasWebSearch := false
	var tools []json.RawMessage
	for _, t := range req.Tools {
		switch {
		case t.Type == "web_search" || t.Type == "web_search_preview" || strings.HasPrefix(t.Type, "web_search"):
			hasWebSearch = true
		case t.Type == "function":
			tools = append(tools, functionToAnthropicTool(t))
		}
	}
	if hasWebSearch {
		// Inject the Anthropic web_search server tool so the loop's
		// DetectWebSearchTool fires and RewriteTools swaps it for the client tool.
		tools = append(tools, json.RawMessage(`{"type":"web_search_20250305","name":"web_search"}`))
	}
	out.Tools = tools

	// Sampling / limits.
	maxTokens := defaultMaxTokens
	if req.MaxOutputTokens != nil && *req.MaxOutputTokens > 0 {
		maxTokens = *req.MaxOutputTokens
	}
	out.Extra["max_tokens"] = mustJSONRaw(maxTokens)
	if req.Temperature != nil {
		out.Extra["temperature"] = mustJSONRaw(*req.Temperature)
	}
	if req.TopP != nil {
		out.Extra["top_p"] = mustJSONRaw(*req.TopP)
	}

	return out, hasWebSearch, nil
}

// argumentsToInput turns a function_call arguments JSON string into a raw JSON
// object for an Anthropic tool_use input. Empty or invalid arguments become {}.
func argumentsToInput(arguments string) json.RawMessage {
	s := strings.TrimSpace(arguments)
	if s == "" {
		return json.RawMessage(`{}`)
	}
	if json.Valid([]byte(s)) {
		return json.RawMessage(s)
	}
	return json.RawMessage(`{}`)
}

// outputToContent turns a function_call_output output into an Anthropic
// tool_result content (a JSON string). The output may already be a JSON string
// value or an arbitrary JSON value; both are represented as a string.
func outputToContent(output json.RawMessage) json.RawMessage {
	if len(output) == 0 {
		return mustJSONRaw("")
	}
	var s string
	if json.Unmarshal(output, &s) == nil {
		return mustJSONRaw(s)
	}
	return mustJSONRaw(string(output))
}

// functionToAnthropicTool builds an Anthropic tool from a Responses function
// tool: {name, description, input_schema}. Parameters becomes input_schema.
func functionToAnthropicTool(t Tool) json.RawMessage {
	obj := map[string]json.RawMessage{
		"name": mustJSONRaw(t.Name),
	}
	if t.Description != "" {
		obj["description"] = mustJSONRaw(t.Description)
	}
	if len(t.Parameters) > 0 {
		obj["input_schema"] = t.Parameters
	} else {
		obj["input_schema"] = json.RawMessage(`{"type":"object","properties":{}}`)
	}
	b, _ := json.Marshal(obj)
	return b
}

// mustJSONRaw marshals any value to json.RawMessage, ignoring the (impossible
// for these inputs) error.
func mustJSONRaw(v interface{}) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
