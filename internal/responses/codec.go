package responses

import "encoding/json"

// DecodeRequest parses a raw /v1/responses body into a Request. The `input`
// field is polymorphic: it may be a bare string (a single user message) or an
// array of input items. A bare string is normalized into a one-element input
// list with a "message" item of role "user".
func DecodeRequest(body []byte) (*Request, error) {
	// Decode everything except input first, keeping input raw so we can handle
	// its string-or-array polymorphism explicitly.
	type alias Request
	var a struct {
		alias
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(body, &a); err != nil {
		return nil, err
	}
	req := Request(a.alias)

	if len(a.Input) > 0 {
		items, err := decodeInput(a.Input)
		if err != nil {
			return nil, err
		}
		req.Input = items
	}
	return &req, nil
}

// decodeInput accepts either a bare string (→ single user message item) or an
// array of input items.
func decodeInput(raw json.RawMessage) ([]InputItem, error) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []InputItem{{
			Type:    "message",
			Role:    "user",
			Content: raw,
		}}, nil
	}
	var items []InputItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	return items, nil
}

// contentText flattens a message item's Content (bare string or an array of
// input_text/output_text parts) into a single plain string.
func (it InputItem) contentText() string {
	if len(it.Content) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(it.Content, &s) == nil {
		return s
	}
	var parts []ContentPart
	if json.Unmarshal(it.Content, &parts) == nil {
		out := ""
		for _, p := range parts {
			out += p.Text
		}
		return out
	}
	return ""
}
