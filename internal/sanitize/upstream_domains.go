package sanitize

import (
	"regexp"
	"strings"
)

// SensitiveDomains lists upstream provider domains that should be removed from
// error responses to avoid leaking internal routing configuration.
var SensitiveDomains = []string{
	"zjz-ai.webtrn.cn",
	"dasheng.aibeke.com",
}

// domainPattern matches URLs containing any of the sensitive domains.
// Built once at package init for efficiency.
var domainPattern *regexp.Regexp

func init() {
	// Build a regex that matches http(s)://domain or just domain in text
	var patterns []string
	for _, domain := range SensitiveDomains {
		// Escape dots in domain names for regex
		escaped := strings.ReplaceAll(domain, ".", `\.`)
		// Match full URLs or bare domains
		patterns = append(patterns, `https?://`+escaped+`[^\s"']*`)
		patterns = append(patterns, escaped)
	}
	domainPattern = regexp.MustCompile(`(?i)` + strings.Join(patterns, "|"))
}

// RemoveUpstreamDomains strips sensitive upstream provider domain names from
// error response bodies. It replaces matching URLs and domains with "[upstream]"
// to prevent leaking internal routing configuration to end users.
//
// This function is idempotent: calling it multiple times on the same input
// produces the same result.
func RemoveUpstreamDomains(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	return domainPattern.ReplaceAll(body, []byte("[upstream]"))
}
