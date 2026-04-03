package awsproxy

import (
	"path/filepath"
	"strings"

	"github.com/wjzhangq/claude-gateway/config"
)

// DefaultPricing is used as a fallback when model_pricing has no matching entry.
var DefaultPricing = config.ModelPricingEntry{
	Input:      3.00,
	Output:     15.00,
	CacheRead:  0.30,
	CacheWrite: 3.75,
}

// ResolvePricing looks up model_pricing by glob pattern.
// Patterns are matched against the lowercase model name.
// The first matching entry is returned; DefaultPricing is used if none match.
func ResolvePricing(model string, pricing map[string]config.ModelPricingEntry) config.ModelPricingEntry {
	m := strings.ToLower(model)
	for pattern, entry := range pricing {
		if matched, _ := filepath.Match(pattern, m); matched {
			return entry
		}
	}
	return DefaultPricing
}

// AWSCostUSD calculates the cost in USD for an AWS Bedrock request.
// Pricing is per 1M tokens.
func AWSCostUSD(model string, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens int,
	pricing map[string]config.ModelPricingEntry) float64 {

	p := ResolvePricing(model, pricing)

	cost := float64(inputTokens)*p.Input +
		float64(outputTokens)*p.Output +
		float64(cacheReadTokens)*p.CacheRead +
		float64(cacheWriteTokens)*p.CacheWrite

	return cost / 1_000_000
}
