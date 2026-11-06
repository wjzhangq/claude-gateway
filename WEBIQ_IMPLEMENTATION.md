# WebSearch WebIQ Provider Implementation

## Summary

Added support for Microsoft AI Web Search API (WebIQ) as a second provider for the gateway's web_search server tool simulation, alongside the existing SearXNG provider.

## Changes

### New Files

1. **internal/websearch/webiq.go**
   - WebIQClient implementation
   - POST-based API calls to Microsoft AI endpoint
   - Automatic adult content filtering
   - Passage-optimized content extraction
   - Domain allow/block filtering (uses `domain` field for accuracy since `url` may be a redirect)
   - URL decoding: extracts real URLs from Bing redirect links (`u=a1<base64>` → original URL)

2. **internal/websearch/webiq_test.go**
   - Unit tests for WebIQ client
   - Tests for search, adult filtering, and domain filtering
   - Mock HTTP server for isolated testing
   - Optional live API integration test (requires WEBIQ_API_KEY env var)

3. **internal/websearch/factory.go**
   - Provider factory function `NewSearchProvider()`
   - Supports both "searxng" and "webiq" providers

4. **internal/websearch/url_decode_test.go**
   - Tests for Bing redirect URL decoding logic
   - Validates base64 decoding and fallback behavior

4. **docs/websearch-providers.md**
   - Configuration guide for both providers
   - API comparison table
   - Switching instructions

### Modified Files

1. **internal/websearch/types.go**
   - Added `SearchProvider` interface
   - Both SearXNG and WebIQ clients implement this interface

2. **internal/proxy/handler.go**
   - Changed `searxng *websearch.Client` field to `searchProvider websearch.SearchProvider`
   - Updated initialization in `NewHandler()` to use factory
   - Updated `UpdateConfig()` for hot-reloading with provider selection

3. **internal/proxy/websearch_loop.go**
   - Updated signature to accept `SearchProvider` interface instead of concrete type

4. **internal/proxy/responses_handler.go**
   - Updated to use `searchProvider` field instead of `searxng`

5. **internal/proxy/responses_handler_test.go**
   - Fixed test to use `searchProvider` field

6. **config/config.yaml** (not tracked by git)
   - Updated to use WebIQ provider
   - Added API key: `TwtrR2l4NMFbKDr3MS7fbgjuYgAgVeP39yAC3RH3N9U=`

## Configuration

### WebIQ (Current)

```yaml
websearch:
  enabled: true
  provider: webiq
  search_url: "https://api.microsoft.ai/v3/search/web"
  authorization: "TwtrR2l4NMFbKDr3MS7fbgjuYgAgVeP39yAC3RH3N9U="
  language: "zh-CN"
  max_results: 12
  snippet_max_chars: 8000
  timeout: 10s
  default_max_uses: 10
```

### SearXNG (Previous)

```yaml
websearch:
  enabled: true
  provider: searxng
  search_url: "https://websearch2.zhangwenjin.com/search"
  authorization: "Bearer cloudflarewebsearch"
  language: "zh-CN"
  max_results: 12
  snippet_max_chars: 8000
  timeout: 10s
  default_max_uses: 10
```

## Key Differences

| Aspect | SearXNG | WebIQ |
|--------|---------|-------|
| Request | GET with query params | POST with JSON body |
| Auth | `Authorization: Bearer <token>` | `x-apikey: <key>` |
| Content | Full snippets | Passage extraction |
| Adult Filter | None | Automatic |
| Cost | Free (self-hosted) | Paid |

## Testing

All tests passing:

```bash
go test ./internal/websearch -v
# 39 tests PASS

go test ./internal/proxy -v
# All tests PASS
```

## Verified

- ✅ WebIQ API endpoint works with provided key
- ✅ Both providers implement same interface
- ✅ Factory pattern allows easy provider switching
- ✅ All existing tests still pass
- ✅ New WebIQ tests cover search, filtering, domain rules, URL decoding
- ✅ Hot-reload via SIGHUP supported
- ✅ Backward compatible with existing SearXNG deployments
- ✅ Live API test confirms redirect URLs are decoded to original URLs (e.g., `api.microsoftol.com/ck/a?u=a1...` → `https://claude.com/`)

## Usage

The gateway automatically selects the provider based on `config.yaml`. No code changes needed to switch between providers - just update the config and reload:

```bash
kill -HUP $(pgrep -f gateway)
```
