package websearch

import (
	"fmt"

	"github.com/wjzhangq/claude-gateway/config"
)

// NewSearchProvider builds a SearchProvider from config based on the provider field.
// Supported providers: "searxng", "webiq".
func NewSearchProvider(cfg config.WebSearchConfig) (SearchProvider, error) {
	switch cfg.Provider {
	case "searxng":
		return NewClient(cfg), nil
	case "webiq":
		return NewWebIQClient(cfg), nil
	default:
		return nil, fmt.Errorf("unsupported websearch provider: %q", cfg.Provider)
	}
}
