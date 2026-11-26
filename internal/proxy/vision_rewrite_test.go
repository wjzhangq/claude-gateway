package proxy

import (
	"encoding/json"
	"testing"
)

func TestRewriteVisionImageURLToAnthropic(t *testing.T) {
	body := []byte(`{
		"model":"claude-opus-5-5",
		"messages":[
			{
				"role":"user",
				"content":[
					{"type":"text","text":"What is in this image?"},
					{"type":"image_url","image_url":{"url":"https://example.com/image.png"}}
				]
			}
		]
	}`)
	got, action, count := RewriteVisionToAnthropicFormat(body)

	if action != visionActionConverted {
		t.Errorf("action = %q; want %s", action, visionActionConverted)
	}
	if count != 1 {
		t.Errorf("count = %d; want 1", count)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	messages := parsed["messages"].([]interface{})
	content := messages[0].(map[string]interface{})["content"].([]interface{})
	imagePart := content[1].(map[string]interface{})

	if imagePart["type"] != "image" {
		t.Errorf("image type = %v; want image", imagePart["type"])
	}

	source := imagePart["source"].(map[string]interface{})
	if source["type"] != "url" {
		t.Errorf("source type = %v; want url", source["type"])
	}
	if source["url"] != "https://example.com/image.png" {
		t.Errorf("source url = %v; want https://example.com/image.png", source["url"])
	}
	if _, exists := imagePart["image_url"]; exists {
		t.Errorf("image_url field still present after conversion")
	}
}

func TestRewriteVisionBase64DataURI(t *testing.T) {
	body := []byte(`{
		"model":"claude-opus-5-5",
		"messages":[
			{
				"role":"user",
				"content":[
					{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="}}
				]
			}
		]
	}`)
	got, action, count := RewriteVisionToAnthropicFormat(body)

	if action != visionActionConverted {
		t.Errorf("action = %q; want %s", action, visionActionConverted)
	}
	if count != 1 {
		t.Errorf("count = %d; want 1", count)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	messages := parsed["messages"].([]interface{})
	content := messages[0].(map[string]interface{})["content"].([]interface{})
	imagePart := content[0].(map[string]interface{})

	if imagePart["type"] != "image" {
		t.Errorf("image type = %v; want image", imagePart["type"])
	}

	source := imagePart["source"].(map[string]interface{})
	if source["type"] != "base64" {
		t.Errorf("source type = %v; want base64", source["type"])
	}
	if source["media_type"] != "image/png" {
		t.Errorf("media_type = %v; want image/png", source["media_type"])
	}
	if source["data"] == "" {
		t.Errorf("base64 data is empty")
	}
}

func TestRewriteVisionMultipleImages(t *testing.T) {
	body := []byte(`{
		"model":"claude-opus-5-5",
		"messages":[
			{
				"role":"user",
				"content":[
					{"type":"image_url","image_url":{"url":"https://example.com/img1.png"}},
					{"type":"text","text":"and"},
					{"type":"image_url","image_url":{"url":"https://example.com/img2.jpg"}}
				]
			}
		]
	}`)
	got, action, count := RewriteVisionToAnthropicFormat(body)

	if action != visionActionConverted {
		t.Errorf("action = %q; want %s", action, visionActionConverted)
	}
	if count != 2 {
		t.Errorf("count = %d; want 2", count)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	messages := parsed["messages"].([]interface{})
	content := messages[0].(map[string]interface{})["content"].([]interface{})

	img1 := content[0].(map[string]interface{})
	if img1["type"] != "image" {
		t.Errorf("first image type = %v; want image", img1["type"])
	}

	text := content[1].(map[string]interface{})
	if text["type"] != "text" || text["text"] != "and" {
		t.Errorf("text block was modified")
	}

	img2 := content[2].(map[string]interface{})
	if img2["type"] != "image" {
		t.Errorf("second image type = %v; want image", img2["type"])
	}
}

func TestRewriteVisionTextOnlyUnchanged(t *testing.T) {
	body := []byte(`{
		"model":"claude-opus-5-5",
		"messages":[
			{
				"role":"user",
				"content":[
					{"type":"text","text":"Hello"}
				]
			}
		]
	}`)
	got, action, count := RewriteVisionToAnthropicFormat(body)

	if action != "" {
		t.Errorf("action = %q; want empty (no vision content)", action)
	}
	if count != 0 {
		t.Errorf("count = %d; want 0", count)
	}

	if string(got) != string(body) {
		t.Errorf("text-only body was modified")
	}
}

func TestRewriteVisionNoMessagesUnchanged(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5-5"}`)
	got, action, count := RewriteVisionToAnthropicFormat(body)

	if action != "" {
		t.Errorf("action = %q; want empty", action)
	}
	if count != 0 {
		t.Errorf("count = %d; want 0", count)
	}

	if string(got) != string(body) {
		t.Errorf("body was modified")
	}
}

func TestRewriteVisionMalformedJSONUnchanged(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5-5","messages":[{`)
	got, action, count := RewriteVisionToAnthropicFormat(body)

	if action != "" {
		t.Errorf("action = %q; want empty", action)
	}
	if count != 0 {
		t.Errorf("count = %d; want 0", count)
	}

	if string(got) != string(body) {
		t.Errorf("malformed body was modified")
	}
}

func TestRewriteVisionPreservesOtherFields(t *testing.T) {
	body := []byte(`{
		"model":"claude-opus-5-5",
		"max_tokens":1024,
		"temperature":0.7,
		"messages":[
			{
				"role":"user",
				"content":[
					{"type":"image_url","image_url":{"url":"https://example.com/image.png"}}
				]
			}
		]
	}`)
	got, _, _ := RewriteVisionToAnthropicFormat(body)

	var parsed map[string]interface{}
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	if parsed["model"] != "claude-opus-5-5" {
		t.Errorf("model field changed")
	}
	if parsed["max_tokens"].(float64) != 1024 {
		t.Errorf("max_tokens field changed")
	}
	if parsed["temperature"].(float64) != 0.7 {
		t.Errorf("temperature field changed")
	}
}

func TestVisionRewriteAppliesToModelEmpty(t *testing.T) {
	if visionRewriteAppliesToModel([]string{}, "claude-opus-5-5") {
		t.Errorf("empty list should match no models (whitelist semantics)")
	}
}

func TestVisionRewriteAppliesToModelStar(t *testing.T) {
	if !visionRewriteAppliesToModel([]string{"*"}, "claude-opus-5-5") {
		t.Errorf("'*' should match every model")
	}
}

func TestVisionRewriteAppliesToModelSubstring(t *testing.T) {
	if !visionRewriteAppliesToModel([]string{"opus-5-5"}, "claude-opus-5-5") {
		t.Errorf("pattern substring should match")
	}
}

func TestVisionRewriteAppliesToModelCaseInsensitive(t *testing.T) {
	if !visionRewriteAppliesToModel([]string{"OPUS"}, "claude-opus-5-5") {
		t.Errorf("pattern match should be case-insensitive")
	}
}

func TestVisionRewriteAppliesToModelDoesNotMatch(t *testing.T) {
	if visionRewriteAppliesToModel([]string{"sonnet"}, "claude-opus-5-5") {
		t.Errorf("non-matching pattern should not match")
	}
}

// The handler no longer gates on isClaudeModel, so the whitelist is the only
// thing keeping the conversion away from OpenAI-native models reachable through
// public_providers. A whitelist of Claude models must not match them.
func TestVisionRewriteAppliesToModelExcludesNonClaude(t *testing.T) {
	whitelist := []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-fable-5"}
	for _, m := range []string{"kimi-k3", "gpt-4o", "mimo-v2.5-pro"} {
		if visionRewriteAppliesToModel(whitelist, m) {
			t.Errorf("model %q matched a Claude-only whitelist", m)
		}
	}
}

// fable and mythos were unreachable while the handler gated on isClaudeModel,
// which only knows claude/sonnet/opus/haiku. The whitelist must now reach them.
func TestVisionRewriteAppliesToModelReachesFableAndMythos(t *testing.T) {
	whitelist := []string{"claude-fable-5", "claude-mythos-5"}
	for _, m := range []string{"claude-fable-5-2", "claude-mythos-5"} {
		if !visionRewriteAppliesToModel(whitelist, m) {
			t.Errorf("model %q did not match whitelist %v", m, whitelist)
		}
	}
}

func TestParseDataURI(t *testing.T) {
	tests := []struct {
		uri       string
		wantMedia string
		wantData  string
	}{
		{
			uri:       "data:image/png;base64,ABC123",
			wantMedia: "image/png",
			wantData:  "ABC123",
		},
		{
			uri:       "data:image/jpeg;base64,XYZ789",
			wantMedia: "image/jpeg",
			wantData:  "XYZ789",
		},
		{
			uri:       "data:application/octet-stream;base64,QWERTY",
			wantMedia: "application/octet-stream",
			wantData:  "QWERTY",
		},
		{
			uri:       "not-a-data-uri",
			wantMedia: "",
			wantData:  "",
		},
		{
			uri:       "data:image/png;NOT-base64,ABC",
			wantMedia: "",
			wantData:  "",
		},
	}

	for _, tt := range tests {
		gotMedia, gotData := parseDataURI(tt.uri)
		if gotMedia != tt.wantMedia || gotData != tt.wantData {
			t.Errorf("parseDataURI(%q) = (%q, %q); want (%q, %q)",
				tt.uri, gotMedia, gotData, tt.wantMedia, tt.wantData)
		}
	}
}
