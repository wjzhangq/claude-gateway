package awsproxy

import (
	"fmt"
	"path/filepath"
	"sort"
)

// ResolveModel maps a user-supplied model name to a Bedrock model ARN / ID.
//
// Resolution order:
//  1. Exact match in replace map (requestModel == key)
//  2. Glob match in defaults map; the matched value is then looked up in replace
//  3. Error if nothing matches
func ResolveModel(requestModel string, replace map[string]string, defaults map[string]string) (string, error) {
	// Step 1: exact match
	if arn, ok := replace[requestModel]; ok {
		return arn, nil
	}

	// Step 2: glob pattern in model_default
	for pattern, upstream := range defaults {
		if matched, _ := filepath.Match(pattern, requestModel); matched {
			// upstream is the canonical model name — look it up in replace to get ARN
			if arn, ok := replace[upstream]; ok {
				return arn, nil
			}
			// If not in replace, return upstream as-is (treated as Bedrock model ID)
			return upstream, nil
		}
	}

	return "", fmt.Errorf("model %q not supported on AWS channel", requestModel)
}

// ListAvailableModels returns sorted keys of the model_replace map.
// These are the model names that clients can request via /v1/models.
func ListAvailableModels(replace map[string]string) []string {
	models := make([]string, 0, len(replace))
	for k := range replace {
		models = append(models, k)
	}
	sort.Strings(models)
	return models
}
