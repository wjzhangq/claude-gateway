package responses

import (
	"time"

	"github.com/wjzhangq/claude-gateway/internal/websearch"
)

// ToResponse converts the assembled Anthropic content blocks, aggregated usage
// and stop_reason into a top-level Responses object. The item construction is
// shared with the SSE synthesizer via buildOutputItems.
//
// stopReason mapping:
//   - "max_tokens" → status "incomplete" + incomplete_details.max_output_tokens
//   - everything else → status "completed"
func ToResponse(model string, blocks []websearch.ContentBlock, usage websearch.Usage, stopReason string) *Response {
	items := buildOutputItems(blocks)
	resp := &Response{
		ID:        NewResponseID(),
		Object:    "response",
		CreatedAt: time.Now().Unix(),
		Model:     model,
		Status:    "completed",
		Output:    items,
		Usage:     convertUsage(usage),
		Store:     false,
	}
	if stopReason == "max_tokens" {
		resp.Status = "incomplete"
		resp.IncompleteDetails = &IncompleteDetails{Reason: "max_output_tokens"}
	}
	return resp
}

// buildOutputItems maps Anthropic content blocks to Responses output items:
//   - runs of text blocks → one assistant "message" item with output_text parts
//   - server_tool_use (web_search) → a "web_search_call" item; the paired
//     web_search_tool_result block carries no separate item (info folds into the
//     following text)
//   - tool_use (client function) → a "function_call" item
func buildOutputItems(blocks []websearch.ContentBlock) []OutputItem {
	var items []OutputItem
	var msg *OutputItem // open assistant message accumulating text parts

	flushMsg := func() {
		if msg != nil {
			items = append(items, *msg)
			msg = nil
		}
	}

	for _, b := range blocks {
		switch b.Type {
		case "text":
			if msg == nil {
				msg = &OutputItem{
					Type:   "message",
					ID:     NewMessageID(),
					Status: "completed",
					Role:   "assistant",
				}
			}
			msg.Content = append(msg.Content, OutputContentPart{
				Type:        "output_text",
				Text:        b.Text,
				Annotations: []Annotation{},
			})
		case "server_tool_use":
			flushMsg()
			items = append(items, OutputItem{
				Type:   "web_search_call",
				ID:     b.ID,
				Status: "completed",
				Action: &WebSearchAction{Type: "search", Query: websearch.QueryOf(b)},
			})
		case "web_search_tool_result":
			// No separate output item: the search content is reflected in the
			// assistant text that follows.
			continue
		case "tool_use":
			flushMsg()
			args := "{}"
			if len(b.Input) > 0 {
				args = string(b.Input)
			}
			items = append(items, OutputItem{
				Type:      "function_call",
				ID:        b.ID,
				CallID:    b.ID,
				Status:    "completed",
				Name:      b.Name,
				Arguments: args,
			})
		default:
			// thinking / passthrough blocks have no Responses representation.
			continue
		}
	}
	flushMsg()
	return items
}

// convertUsage maps Anthropic usage to the Responses usage block.
func convertUsage(u websearch.Usage) *Usage {
	return &Usage{
		InputTokens:         u.InputTokens,
		OutputTokens:        u.OutputTokens,
		TotalTokens:         u.InputTokens + u.OutputTokens,
		OutputTokensDetails: OutputTokensDetails{ReasoningTokens: 0},
	}
}

// NewResponseID returns a fresh response id with the Responses "resp_" prefix.
func NewResponseID() string {
	return "resp_" + randID()
}

// NewMessageID returns a fresh message-item id with the "msg_" prefix.
func NewMessageID() string {
	return "msg_" + randID()
}

// randID reuses the websearch id generator's entropy (srvtoolu_-prefixed) and
// strips the prefix, so responses ids share the same random-hex format.
func randID() string {
	id := websearch.NewServerToolID()
	const p = "srvtoolu_"
	if len(id) > len(p) {
		return id[len(p):]
	}
	return id
}
