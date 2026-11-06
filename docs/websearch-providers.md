# WebSearch Provider Configuration

The gateway supports two web search providers for the web_search server tool simulation:

## Providers

### 1. SearXNG (Open Source)

Self-hosted search aggregator.

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

**Key Points:**
- `provider`: Must be `searxng`
- `search_url`: SearXNG instance URL with `/search` path
- `authorization`: Bearer token (format: `Bearer <token>`)
- Uses GET requests with query parameters

### 2. Microsoft AI Web Search (WebIQ)

Commercial API from Microsoft AI platform.

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

**Key Points:**
- `provider`: Must be `webiq`
- `search_url`: Microsoft AI API endpoint
- `authorization`: API key (direct value, no `Bearer` prefix)
- Uses POST requests with JSON payload
- Automatically filters adult content
- Returns passage-optimized content for LLM grounding

## API Comparison

| Feature | SearXNG | WebIQ |
|---------|---------|-------|
| Request Method | GET | POST |
| Auth Header | `Authorization: Bearer <token>` | `x-apikey: <key>` |
| Content Format | Full snippets | Passage extraction |
| Adult Filter | Manual | Automatic |
| Score Ranking | Yes | No (pre-ranked) |
| Cost | Free (self-hosted) | Paid (per request) |

## Configuration Fields

- `enabled`: Master switch (true/false)
- `provider`: Search backend (`searxng` or `webiq`)
- `search_url`: API endpoint URL
- `authorization`: Authentication credential
- `language`: Default search language (e.g., `zh-CN`, `en-US`)
- `max_results`: Maximum results returned to model per search
- `snippet_max_chars`: Content truncation length per result
- `timeout`: Request timeout (e.g., `10s`)
- `default_max_uses`: Per-request search cap when tool omits `max_uses`

## Testing

Run tests to verify both providers work correctly:

```bash
# Test WebIQ client
go test ./internal/websearch -v -run TestWebIQ

# Test SearXNG client  
go test ./internal/websearch -v -run TestSearch

# Run all websearch tests
go test ./internal/websearch -v
```

## Switching Providers

1. Update `config/config.yaml`:
   - Change `provider` to `searxng` or `webiq`
   - Update `search_url` to the correct endpoint
   - Update `authorization` with the appropriate credential format

2. Reload config:
   ```bash
   kill -HUP $(pgrep -f gateway)
   ```

3. The gateway will rebuild the search client automatically without restarting.

## Implementation Details

- Both providers implement the same `SearchProvider` interface
- Domain filtering (allowed/blocked) is applied uniformly
- Results are normalized to a common format before feeding to the model
- The gateway rewrites Anthropic's `web_search_*` server tool to a client tool and intercepts calls
