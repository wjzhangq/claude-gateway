# Thinking Parameter Rewrite

## Overview

The gateway can automatically rewrite the `thinking` parameter in incoming requests
to make older SDK clients compatible with Claude Opus 5.5 / Sonnet 5.5 / Haiku 5.5.

These models reject the pre-4.6 request shapes:
- `thinking.type: "enabled"` (which carried `budget_tokens`) is a 400
- `thinking.type: "disabled"` is a 400 on Opus 5.5 (at every effort level) and Sonnet 5.5
- `budget_tokens` is removed on these models and returns a 400

Clients pinned to an older Anthropic SDK continue sending these values, causing
every request to fail. This feature rewrites them at the gateway so the whole
fleet keeps working while clients upgrade.

## Configuration

Add to `config.yaml`:

```yaml
thinking_rewrite:
  enabled: true
  apply_to_models:
    - "opus-5-5"
    - "sonnet-5-5"
    - "haiku-5-5"
    - "fable-5"
    - "mythos-5"
  log_rewrites: true
```

### Fields

- **`enabled`**: Master switch. When false, no body rewriting is done. Default: `false`.

- **`apply_to_models`**: Model name substrings to match. Only requests whose `model`
  field contains one of these patterns (case-insensitive) will be rewritten.
  - An empty list or `["*"]` matches every model.
  - Pattern matching uses substring containment, so `"opus-5-5"` matches
    `"claude-opus-5-5"`, `"anthropic.claude-opus-5-5"`, etc.

- **`log_rewrites`**: When true, logs one line per rewritten request with:
  - `itcode` - user identifier
  - `model` - the requested model name
  - `original` - the original `thinking.type` value
  - `action` - what the gateway did (`rewrote_to_adaptive` or `deleted`)

## Rewrite Rules

The gateway applies these transformations:

| Original `thinking.type` | Action | Result |
|--------------------------|--------|--------|
| `"enabled"` | Rewrite to `"adaptive"` and drop `budget_tokens` | Valid request |
| `"extended_thinking"` | Rewrite to `"adaptive"` | Valid request |
| `"disabled"` | Delete the `thinking` field | Model applies its default (adaptive) |
| `""` (empty) | Delete the `thinking` field | Model applies its default (adaptive) |
| Any other value | Delete the `thinking` field | Model applies its default (adaptive) |
| `"adaptive"` | No change | Already valid |
| `"between_tools"` | No change | Already valid |

No change is made when:
- The request body is not valid JSON
- There is no `thinking` field
- `thinking` is not an object (e.g., a bare string)
- The rewrite fails for any reason (the original body is forwarded unchanged)

## Logging Output

With `log_rewrites: true`, the gateway emits log lines like:

```
INFO[...] rewrote thinking: itcode=licl48 model=claude-opus-5-5 original=enabled action=rewrote_to_adaptive
INFO[...] rewrote thinking: itcode=pengwx4 model=claude-sonnet-5-5 original=disabled action=deleted
```

Use these logs to:
1. **Measure impact**: count how many requests are being patched
2. **Identify users**: which itcodes are still using old clients
3. **Plan upgrades**: notify high-volume users to upgrade their SDK

Query examples:

```bash
# Total rewrites
grep "rewrote thinking" /data/log/backend-*.log | wc -l

# Per itcode (top 20)
grep "rewrote thinking" /data/log/backend-*.log | \
  grep -oP 'itcode=\K[^ ]+' | sort | uniq -c | sort -rn | head -20

# Per model
grep "rewrote thinking" /data/log/backend-*.log | \
  grep -oP 'model=\K[^ ]+' | sort | uniq -c | sort -rn

# Per action
grep "rewrote thinking" /data/log/backend-*.log | \
  grep -oP 'action=\K[^ ]+' | sort | uniq -c
```

## Validation

After enabling this feature, check:

1. **400 error rate drops**
   - Opus 5.5 / Sonnet 5.5 / Haiku 5.5 `400` errors should decrease significantly
   - Remaining 400s should be from other causes (security refusals, overlong, etc.)

2. **Rewrite log volume**
   - The count of "rewrote thinking" log lines shows how many legacy requests
     the gateway is fixing

3. **User feedback**
   - Users who reported `thinking.type` 400 errors should stop seeing them

## Deployment

1. Build the updated gateway binary: `go build -o claude-gateway ./cmd/server`

2. Add the `thinking_rewrite` block to `config.yaml` (see Configuration above)

3. Restart the service

4. Monitor logs: `tail -f /data/log/backend-*.log | grep "rewrote thinking"`

## Rollback

If unexpected issues arise, disable the feature by editing `config.yaml`:

```yaml
thinking_rewrite:
  enabled: false  # <-- set to false
```

Then restart the service. The feature has zero effect when disabled.

## Removal Plan

Once enough users have upgraded:

1. **Identify remaining users**: Query logs for itcodes still triggering rewrites

   ```bash
   find /data/log -name "backend-*.log" -mtime -7 -exec \
     grep "rewrote thinking" {} \; | \
     grep -oP 'itcode=\K[^ ]+' | sort -u
   ```

2. **Send upgrade notices** to those itcodes

3. **Monitor adoption**: Track the rewrite rate over 2-4 weeks

4. **Disable the feature**: When the rate drops to near-zero, set `enabled: false`
   to remove the overhead

## Implementation Notes

- Uses `gjson`/`sjson` for surgical JSON editing (preserves key order, number
  formatting, and large integers)
- Never corrupts the request: on any error, the original body is forwarded unchanged
- Independent of other body-rewriting features (e.g. `InjectStreamOptions`)
- Runs only on the backend proxy channel (not the public proxy channel)
- Zero overhead when disabled

