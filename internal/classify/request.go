package classify

import (
	"encoding/json"
	"errors"
)

// Request is the subset of an Anthropic /v1/messages request we care about.
type Request struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
}

// Message is one turn. Content is either a plain string or an array of blocks;
// UnmarshalJSON normalizes both into Blocks (a string becomes a single text block).
type Message struct {
	Role   string
	Blocks []ContentBlock
}

// ContentBlock is one content element. Only the fields relevant to signal
// extraction are kept; everything else (images, schemas) is ignored.
type ContentBlock struct {
	Type string `json:"type"` // text | tool_use | tool_result | ...
	Text string `json:"text"` // for type=text

	// tool_use fields
	Name  string          `json:"name"`  // tool name, e.g. "Edit", "Bash"
	Input json.RawMessage `json:"input"` // tool arguments (file_path, command, ...)
}

// rawMessage mirrors Message but keeps Content as raw JSON so we can accept both
// the string and the array-of-blocks encodings Anthropic permits.
type rawMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// UnmarshalJSON accepts both content encodings: a bare string (→ one text block)
// or an array of content blocks.
func (m *Message) UnmarshalJSON(data []byte) error {
	var rm rawMessage
	if err := json.Unmarshal(data, &rm); err != nil {
		return err
	}
	m.Role = rm.Role
	m.Blocks = nil
	if len(rm.Content) == 0 {
		return nil
	}
	// Fast path: string content.
	var s string
	if json.Unmarshal(rm.Content, &s) == nil {
		if s != "" {
			m.Blocks = []ContentBlock{{Type: "text", Text: s}}
		}
		return nil
	}
	// Array-of-blocks content.
	var blocks []ContentBlock
	if err := json.Unmarshal(rm.Content, &blocks); err != nil {
		return err
	}
	m.Blocks = blocks
	return nil
}

// ErrNoMessages is returned when a body parses as JSON but carries no messages.
var ErrNoMessages = errors.New("classify: request has no messages")

// ParseRequest decodes a raw request body into a Request. A truncated body,
// non-JSON, or a body with zero messages returns an error so the caller can mark
// the record unparseable rather than silently mis-classify it.
func ParseRequest(body []byte) (Request, error) {
	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		return Request{}, err
	}
	if len(req.Messages) == 0 {
		return Request{}, ErrNoMessages
	}
	return req, nil
}
