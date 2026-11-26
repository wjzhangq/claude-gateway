package proxy

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Vision rewrite actions.
const (
	visionActionConverted = "converted"
)

// RewriteVisionToAnthropicFormat checks if the request body contains OpenAI-format
// vision content (image_url) and converts it to Anthropic format (image with source).
//
// OpenAI format:
//
//	{"type": "image_url", "image_url": {"url": "..."}}
//	or with base64: {"type": "image_url", "image_url": {"url": "data:image/png;base64,..."}}
//
// Anthropic format:
//
//	{"type": "image", "source": {"type": "url", "url": "..."}}
//	or {"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "..."}}
//
// The function:
// 1. Scans messages[].content[] for any "image_url" type entries
// 2. For each found, extracts the URL and converts to Anthropic format
// 3. Preserves all other content and message structure
// 4. Returns original body unchanged if not valid JSON or on any rewrite error
//
// The scan is the cheap path: a body with no image_url block — nearly all
// traffic — is returned as the caller's own slice with no allocation and no
// rewrite, so enabling the feature does not change text-only requests.
//
// Returns: (rewritten body, action, converted count)
//   - action is "" and count 0 if nothing was converted; the returned body is
//     then the caller's own slice
//   - action is "converted" if one or more blocks were converted, and count is
//     how many
func RewriteVisionToAnthropicFormat(body []byte) (out []byte, action string, count int) {
	// Validate JSON first to avoid gjson reading garbage out of truncated input.
	if !gjson.ValidBytes(body) {
		return body, "", 0
	}

	// Scan for any image_url entries in messages[].content[].
	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return body, "", 0
	}

	// Collect all (messageIndex, contentIndex) pairs that need conversion.
	// gjson's ForEach does not report an array index, so track both levels
	// manually; the indexes have to match the sjson paths used below exactly.
	type conversionTarget struct {
		msgIdx int
		cIdx   int
	}
	var targets []conversionTarget

	msgIdx := -1
	messages.ForEach(func(_, msgVal gjson.Result) bool {
		msgIdx++
		if !msgVal.IsObject() {
			return true // continue
		}

		content := msgVal.Get("content")
		if !content.Exists() || !content.IsArray() {
			return true // continue
		}

		cIdx := -1
		content.ForEach(func(_, contentVal gjson.Result) bool {
			cIdx++
			if !contentVal.IsObject() {
				return true // continue
			}

			if contentVal.Get("type").String() == "image_url" {
				targets = append(targets, conversionTarget{msgIdx, cIdx})
			}
			return true // continue
		})

		return true // continue
	})

	// If no image_url found, return unchanged.
	if len(targets) == 0 {
		return body, "", 0
	}

	// Vision content detected; now convert each target.
	newBody := body
	var err error
	convertCount := 0

	// Process in reverse order to avoid index shifting.
	for i := len(targets) - 1; i >= 0; i-- {
		t := targets[i]
		pathPrefix := fmt.Sprintf("messages.%d.content.%d", t.msgIdx, t.cIdx)

		// Extract the original image_url structure.
		imageURLObj := gjson.GetBytes(newBody, pathPrefix+".image_url")
		if !imageURLObj.Exists() {
			continue // Safety check; should not happen if our scan was correct.
		}

		url := imageURLObj.Get("url").String()
		if url == "" {
			continue // Skip entries with no URL.
		}

		// Determine if this is a base64 data URI or a regular URL.
		if strings.HasPrefix(url, "data:") {
			// Parse data URI: "data:image/png;base64,<base64_data>"
			mediaType, data := parseDataURI(url)
			if data == "" {
				continue // Invalid data URI; skip.
			}

			// Convert to Anthropic base64 format.
			newBody, err = sjson.SetBytes(newBody, pathPrefix+".type", "image")
			if err != nil {
				return body, "", 0
			}
			newBody, err = sjson.DeleteBytes(newBody, pathPrefix+".image_url")
			if err != nil {
				return body, "", 0
			}
			newBody, err = sjson.SetBytes(newBody, pathPrefix+".source", map[string]interface{}{
				"type":       "base64",
				"media_type": mediaType,
				"data":       data,
			})
			if err != nil {
				return body, "", 0
			}
		} else {
			// Regular URL; convert to Anthropic url format.
			newBody, err = sjson.SetBytes(newBody, pathPrefix+".type", "image")
			if err != nil {
				return body, "", 0
			}
			newBody, err = sjson.DeleteBytes(newBody, pathPrefix+".image_url")
			if err != nil {
				return body, "", 0
			}
			newBody, err = sjson.SetBytes(newBody, pathPrefix+".source", map[string]interface{}{
				"type": "url",
				"url":  url,
			})
			if err != nil {
				return body, "", 0
			}
		}

		convertCount++
	}

	if convertCount > 0 {
		return newBody, visionActionConverted, convertCount
	}

	return body, "", 0
}

// parseDataURI extracts media type and base64 data from a data URI.
// Format: "data:image/png;base64,<base64_data>" or "data:application/octet-stream;base64,<data>"
// Returns (mediaType, base64Data).
func parseDataURI(uri string) (string, string) {
	// Remove "data:" prefix.
	if !strings.HasPrefix(uri, "data:") {
		return "", ""
	}
	rest := uri[5:]

	// Find the comma separating metadata from data.
	parts := strings.Split(rest, ",")
	if len(parts) != 2 {
		return "", ""
	}

	metadata := parts[0]
	data := parts[1]

	// Ensure metadata contains ";base64" before we extract media type.
	if !strings.Contains(metadata, ";base64") {
		return "", "" // Only base64 encoding is supported.
	}

	// metadata is typically "image/png;base64" or "application/octet-stream;base64"
	// Extract media type (everything before the semicolon).
	mediaType := strings.Split(metadata, ";")[0]

	return mediaType, data
}

// visionRewriteAppliesToModel reports whether the rewrite should run for this model.
// Same pattern as thinkingRewriteApplies.
func visionRewriteAppliesToModel(patterns []string, model string) bool {
	if len(patterns) == 0 {
		return true
	}
	m := strings.ToLower(model)
	for _, p := range patterns {
		if p == "*" {
			return true
		}
		if p == "" {
			continue
		}
		if strings.Contains(m, strings.ToLower(p)) {
			return true
		}
	}
	return false
}
