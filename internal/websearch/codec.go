package websearch

import "encoding/json"

// reservedTopFields are the request fields represented by typed struct fields;
// they are pulled out of Extra on decode and re-added on encode.
var reservedTopFields = []string{"model", "messages", "tools", "stream"}

// DecodeRequest parses a raw /v1/messages body into a MessagesRequest, keeping
// every unrecognized top-level field in Extra so the request can be forwarded
// upstream without losing system / thinking / tool_choice / metadata / etc.
func DecodeRequest(body []byte) (*MessagesRequest, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return nil, err
	}
	req := &MessagesRequest{Extra: map[string]json.RawMessage{}}
	for k, v := range top {
		req.Extra[k] = v
	}
	if raw, ok := top["model"]; ok {
		_ = json.Unmarshal(raw, &req.Model)
	}
	if raw, ok := top["stream"]; ok {
		_ = json.Unmarshal(raw, &req.Stream)
	}
	if raw, ok := top["messages"]; ok {
		if err := json.Unmarshal(raw, &req.Messages); err != nil {
			return nil, err
		}
	}
	if raw, ok := top["tools"]; ok {
		_ = json.Unmarshal(raw, &req.Tools)
	}
	for _, k := range reservedTopFields {
		delete(req.Extra, k)
	}
	return req, nil
}

// Encode serializes the request back to JSON, merging the typed fields over
// Extra. Tools is omitted entirely when empty (so a request whose only tool was
// web_search, if it were ever fully stripped, wouldn't send an empty array).
func (r *MessagesRequest) Encode() ([]byte, error) {
	out := map[string]json.RawMessage{}
	for k, v := range r.Extra {
		out[k] = v
	}
	if b, err := json.Marshal(r.Model); err == nil {
		out["model"] = b
	}
	if b, err := json.Marshal(r.Stream); err == nil {
		out["stream"] = b
	}
	msgs, err := json.Marshal(r.Messages)
	if err != nil {
		return nil, err
	}
	out["messages"] = msgs
	if len(r.Tools) > 0 {
		if b, err := json.Marshal(r.Tools); err == nil {
			out["tools"] = b
		}
	} else {
		delete(out, "tools")
	}
	return json.Marshal(out)
}

// rawMessage mirrors an Anthropic message with content kept raw so both the
// bare-string and array-of-blocks encodings are accepted.
type rawMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// UnmarshalJSON accepts both content encodings: a bare string (→ one text
// block) or an array of content blocks. A string turn is remembered so it can
// be re-emitted as a string on marshal, preserving the original wire shape.
func (m *Message) UnmarshalJSON(data []byte) error {
	var rm rawMessage
	if err := json.Unmarshal(data, &rm); err != nil {
		return err
	}
	m.Role = rm.Role
	m.Blocks = nil
	m.wasText = false
	if len(rm.Content) == 0 {
		return nil
	}
	var s string
	if json.Unmarshal(rm.Content, &s) == nil {
		m.rawText = s
		m.wasText = true
		if s != "" {
			m.Blocks = []ContentBlock{{Type: "text", Text: s}}
		}
		return nil
	}
	return json.Unmarshal(rm.Content, &m.Blocks)
}

// MarshalJSON re-emits the message. A turn that arrived as a bare string and was
// not modified is emitted as a string again; otherwise content is an array of
// blocks.
func (m Message) MarshalJSON() ([]byte, error) {
	if m.wasText {
		return json.Marshal(struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}{m.Role, m.rawText})
	}
	return json.Marshal(struct {
		Role    string         `json:"role"`
		Content []ContentBlock `json:"content"`
	}{m.Role, m.Blocks})
}

// UnmarshalJSON decodes a content block, retaining the raw bytes for block types
// the loop doesn't rewrite (e.g. thinking / image) so they round-trip verbatim.
func (b *ContentBlock) UnmarshalJSON(data []byte) error {
	type alias ContentBlock
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*b = ContentBlock(a)
	switch b.Type {
	case "text", "tool_use", "tool_result", "server_tool_use", "web_search_tool_result":
		// typed blocks: fields captured above
	default:
		b.Raw = append(json.RawMessage(nil), data...)
	}
	return nil
}

// MarshalJSON emits a passthrough block verbatim (Raw), otherwise emits the
// typed fields.
func (b ContentBlock) MarshalJSON() ([]byte, error) {
	if len(b.Raw) > 0 {
		return b.Raw, nil
	}
	type alias ContentBlock
	return json.Marshal(alias(b))
}
