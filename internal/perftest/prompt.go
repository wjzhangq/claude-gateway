package perftest

import (
	"fmt"
	"strings"
)

const wordsPerToken = 0.75

var filler = []string{
	"the", "quick", "brown", "fox", "jumps", "over", "lazy", "dog",
	"in", "a", "land", "far", "away", "there", "lived", "many",
	"people", "who", "loved", "to", "read", "books", "and", "write",
	"stories", "about", "great", "adventures", "across", "the", "world",
	"every", "day", "they", "would", "gather", "around", "fire", "tell",
}

func GeneratePrompt(targetTokens int) string {
	words := int(float64(targetTokens) / wordsPerToken)
	if words < 10 {
		words = 10
	}
	var sb strings.Builder
	for i := 0; i < words; i++ {
		if i > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteString(filler[i%len(filler)])
	}
	return sb.String()
}

func SystemPrompt(maxTokens int) string {
	return fmt.Sprintf("You are a helpful assistant. Please respond with exactly %d tokens of continuous prose. Output random meaningful English text continuously without stopping until you reach the token limit.", maxTokens)
}

func BuildAnthropicBody(inputTokens, maxTokens int, model string) map[string]interface{} {
	return map[string]interface{}{
		"model":      model,
		"max_tokens": maxTokens,
		"stream":     true,
		"system":     SystemPrompt(maxTokens),
		"messages": []map[string]string{
			{"role": "user", "content": GeneratePrompt(inputTokens)},
		},
	}
}

func BuildOpenAIBody(inputTokens, maxTokens int, model string) map[string]interface{} {
	return map[string]interface{}{
		"model":      model,
		"max_tokens": maxTokens,
		"stream":     true,
		"messages": []map[string]interface{}{
			{"role": "system", "content": SystemPrompt(maxTokens)},
			{"role": "user", "content": GeneratePrompt(inputTokens)},
		},
	}
}
