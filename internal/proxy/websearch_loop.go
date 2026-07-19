package proxy

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wjzhangq/claude-gateway/config"
	"github.com/wjzhangq/claude-gateway/internal/logger"
	"github.com/wjzhangq/claude-gateway/internal/websearch"
)

// maxWebSearchRounds hard-caps the number of upstream round-trips the loop makes
// regardless of the request's max_uses, bounding worst-case latency and cost if
// a model keeps calling web_search.
const maxWebSearchRounds = 12

// loopResult is the protocol-neutral output of runWebSearchLoop: the assembled
// content blocks, aggregated usage, the final stop_reason and the effective
// upstream model. Callers format this into their own wire protocol (Anthropic
// Messages or OpenAI Responses).
type loopResult struct {
	Merged     []websearch.ContentBlock
	Usage      websearch.Usage
	StopReason string
	Model      string
}

// loopError carries a loop failure in a protocol-neutral way so each caller can
// render it in its own wire format. Exactly one condition is set:
//   - Canceled: the client disconnected; the caller should return without writing.
//   - Upstream != nil (via RawBody/Status/ContentType): a first-round upstream
//     error to relay verbatim.
//   - otherwise Message describes a synthesized gateway error (502-style).
type loopError struct {
	Canceled    bool
	Message     string
	RawBody     []byte // first-round upstream error body (already de-gzipped)
	Status      int    // upstream status for a passthrough error
	ContentType string // upstream Content-Type for a passthrough error
	Passthrough bool   // true when RawBody/Status/ContentType should be relayed
}

// handleWithWebSearch runs the gateway web-search agent loop for a backend
// /v1/messages request that declared the web_search server tool, then writes the
// result to the client as Anthropic Messages — JSON (non-stream) or a
// synthesized Anthropic SSE stream (stream). The loop itself lives in the
// protocol-neutral runWebSearchLoop; this is the Anthropic-protocol caller.
// Usage is aggregated across rounds and billed once through emitUsage.
func (h *Handler) handleWithWebSearch(c *gin.Context, backend *Backend, upstreamPath string, body []byte, reqModel string, keyInfo interface{}, keyStr string, start time.Time, isOpenClaw, isHermes bool) {
	h.mu.RLock()
	client := h.searxng
	cfg := h.webSearchCfg
	h.mu.RUnlock()

	req, err := websearch.DecodeRequest(body)
	if err != nil {
		logger.Warnf("websearch: decode request failed, falling back to passthrough: %v", err)
		h.forwardPlain(c, backend, upstreamPath, body, reqModel, keyInfo, keyStr, start, isOpenClaw, isHermes)
		return
	}

	clientWantsStream := req.Stream

	// Rewrite for upstream: server tool -> client tool, normalize prior gateway
	// blocks, force non-streaming rounds. Done here (not in the core) so the core
	// stays protocol-agnostic and callers control history normalization.
	req.NormalizeHistory()
	req.RewriteTools()
	req.Stream = false

	// SSE setup: start the stream immediately so pings during searches keep the
	// client alive while the loop runs.
	var sse *sseWriter
	if clientWantsStream {
		sse = newSSEWriter(c)
	}
	var pinger func()
	if sse != nil {
		pinger = sse.ping
	}

	res, lerr := h.runWebSearchLoop(c, backend, upstreamPath, reqModel, req, client, cfg, start, pinger)
	if lerr != nil {
		if lerr.Canceled {
			return
		}
		if lerr.Passthrough {
			h.webSearchPassRawError(c, sse, lerr)
			return
		}
		h.webSearchError(c, sse, lerr.Message, nil)
		return
	}

	h.emitUsage(keyInfo, keyStr, backend.Name, reqModel, http.StatusOK,
		res.Usage.InputTokens, res.Usage.OutputTokens, time.Since(start),
		isOpenClaw, isHermes, false, c.Request.Header.Get("User-Agent"), c.ClientIP(), body)

	if sse != nil {
		sse.writeResponse(res.Model, res.Merged, res.Usage, res.StopReason)
	} else {
		writeJSONResponse(c, res.Model, res.Merged, res.Usage, res.StopReason)
	}
}

// runWebSearchLoop is the protocol-neutral web-search agent loop. It expects a
// request already prepared for upstream (server tool rewritten to client tool,
// history normalized, Stream=false) and runs up to maxWebSearchRounds
// non-streaming upstream calls, resolving the model's web_search tool_use calls
// against SearXNG and feeding results back until the model stops searching, a
// client tool is called, or the per-request cap is reached.
//
// It returns a protocol-neutral loopResult the caller renders into its own wire
// format, or a loopError describing a cancellation / upstream / gateway failure.
// It handles backend health accounting internally but does NOT bill usage or
// write to the client — that is the caller's job.
//
// When the request declares no web_search client tool (e.g. an OpenAI Responses
// request that only uses function tools), no search is ever triggered: the model
// simply won't emit a web_search call, so the loop runs a single round and
// returns the plain content.
func (h *Handler) runWebSearchLoop(c *gin.Context, backend *Backend, upstreamPath, reqModel string, req *websearch.MessagesRequest, client *websearch.Client, cfg config.WebSearchConfig, start time.Time, pinger func()) (loopResult, *loopError) {
	_, detectedMaxUses, allowed, blocked := websearch.DetectWebSearchTool(req.Tools)
	maxUses := detectedMaxUses
	if maxUses < 0 {
		maxUses = cfg.DefaultMaxUses
	}
	if maxUses <= 0 {
		maxUses = 1
	}

	logger.Infof("websearch: start model=%s backend=%s max_uses=%d allowed_domains=%d blocked_domains=%d",
		reqModel, backend.Name, maxUses, len(allowed), len(blocked))

	targetURL := strings.TrimRight(backend.URL, "/") + upstreamPath
	var merged []websearch.ContentBlock
	var total websearch.Usage
	stopReason := "end_turn"
	respModel := reqModel
	uses := 0

	for round := 0; round < maxWebSearchRounds; round++ {
		roundBody, err := req.Encode()
		if err != nil {
			return loopResult{}, &loopError{Message: "encode request failed"}
		}

		resp, err := h.doRequest(c, backend, upstreamPath, targetURL, roundBody)
		if err != nil {
			if IsClientCanceled(err) {
				backend.RecordResult(ErrCanceled)
				return loopResult{}, &loopError{Canceled: true}
			}
			backend.RecordResult(ClassifyError(0, err))
			return loopResult{}, &loopError{Message: "upstream request failed"}
		}

		parsed, raw, rawStatus, ok := readMessagesResponse(resp)
		backend.RecordRequest(rawStatus, time.Since(start).Milliseconds())
		if isValidHealthModel(reqModel) {
			backend.RecordResultDetailed(ClassifyError(rawStatus, nil), 0, false)
		}
		if !ok || rawStatus >= 400 {
			// Upstream error: surface it verbatim on the first round, else stop
			// the loop with what we have.
			if round == 0 {
				return loopResult{}, &loopError{
					Passthrough: true,
					RawBody:     raw,
					Status:      rawStatus,
					ContentType: resp.Header.Get("Content-Type"),
				}
			}
			logger.Warnf("websearch: upstream error status=%d mid-loop, stopping", rawStatus)
			break
		}

		total.Add(parsed.Usage)
		if parsed.Model != "" {
			respModel = parsed.Model
		}

		webCalls, clientCalls, others := websearch.SplitResponse(parsed.Content)
		merged = append(merged, others...)

		if len(webCalls) == 0 {
			stopReason = parsed.StopReason
			if len(clientCalls) > 0 {
				merged = append(merged, clientCalls...)
				stopReason = "tool_use"
			}
			break
		}

		// Resolve each web_search call.
		capReached := uses >= maxUses
		for _, call := range webCalls {
			query := websearch.QueryOf(call)
			srvID := websearch.NewServerToolID()
			merged = append(merged, websearch.ServerToolUseBlock(srvID, query))

			if capReached {
				// Over the per-request cap: don't search. Emit a max_uses_exceeded
				// result to the client and tell the model to finish from what it has.
				merged = append(merged, websearch.ErrorBlock(srvID, "max_uses_exceeded"))
				appendToolRound(req, call.ID, query, websearch.MaxUsesExceededResult(), true)
				continue
			}

			if pinger != nil {
				pinger()
			}
			searchStart := time.Now()
			results, searchErr := client.Search(c.Request.Context(), query, cfg.Language, allowed, blocked)
			searchMs := time.Since(searchStart).Milliseconds()
			if searchErr != nil {
				logger.Warnf("websearch: query=%q round=%d use=%d elapsed_ms=%d failed: %v",
					query, round, uses+1, searchMs, searchErr)
			} else {
				logger.Infof("websearch: query=%q round=%d use=%d elapsed_ms=%d results=%d",
					query, round, uses+1, searchMs, len(results))
			}
			uses++
			if total.ServerToolUse == nil {
				total.ServerToolUse = &websearch.ServerToolUse{}
			}
			total.ServerToolUse.WebSearchRequests++

			merged = append(merged, websearch.WebSearchResultBlock(srvID, results, searchErr))

			// Feed the model: standard tool_use in the assistant turn, tool_result
			// in a following user turn.
			appendToolRound(req, call.ID, query, websearch.FormatForModel(results, searchErr), searchErr != nil)
		}

		// Any client tools alongside web_search: hand back to the client now.
		if len(clientCalls) > 0 {
			merged = append(merged, clientCalls...)
			stopReason = "tool_use"
			break
		}
		if capReached {
			// One final round already fed the max_uses notice; let the model
			// produce its closing answer next iteration, but if we've hit the cap
			// we stop after that round via the len(webCalls)==0 path.
			continue
		}
	}

	webSearchRequests := 0
	if total.ServerToolUse != nil {
		webSearchRequests = total.ServerToolUse.WebSearchRequests
	}
	logger.Infof("websearch: done model=%s backend=%s searches=%d stop=%s total_ms=%d input_tokens=%d output_tokens=%d",
		reqModel, backend.Name, webSearchRequests, stopReason,
		time.Since(start).Milliseconds(), total.InputTokens, total.OutputTokens)

	return loopResult{Merged: merged, Usage: total, StopReason: stopReason, Model: respModel}, nil
}

// appendToolRound appends the assistant tool_use turn and the user tool_result
// turn for one resolved search, so the next upstream round sees the results.
func appendToolRound(req *websearch.MessagesRequest, toolUseID, query, resultText string, isErr bool) {
	input, _ := json.Marshal(websearch.WebSearchInput{Query: query})
	req.Messages = append(req.Messages,
		websearch.Message{Role: "assistant", Blocks: []websearch.ContentBlock{{
			Type:  "tool_use",
			ID:    toolUseID,
			Name:  websearch.ClientToolName,
			Input: input,
		}}},
		websearch.Message{Role: "user", Blocks: []websearch.ContentBlock{{
			Type:      "tool_result",
			ToolUseID: toolUseID,
			Content:   mustJSONString(resultText),
			IsError:   isErr,
		}}},
	)
}

func mustJSONString(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

// readMessagesResponse reads and decodes an upstream non-streaming /v1/messages
// response, transparently gunzipping. Returns the parsed message, the raw
// (de-gzipped) body, the HTTP status, and whether decoding succeeded. The raw
// body is returned so an upstream error can be relayed to the client verbatim
// without re-reading the (already consumed) response body.
func readMessagesResponse(resp *http.Response) (*websearch.MessagesResponse, []byte, int, bool) {
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, resp.StatusCode, false
	}
	if resp.Header.Get("Content-Encoding") == "gzip" {
		if r, e := gzip.NewReader(bytes.NewReader(raw)); e == nil {
			if dec, e2 := io.ReadAll(r); e2 == nil {
				raw = dec
			}
		}
	}
	var parsed websearch.MessagesResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, raw, resp.StatusCode, false
	}
	return &parsed, raw, resp.StatusCode, true
}

// forwardPlain re-forwards the original request as a normal passthrough when the
// web-search loop can't run (e.g. body decode failure). It mirrors the tail of
// forward(): single request, then stream or buffer.
func (h *Handler) forwardPlain(c *gin.Context, backend *Backend, upstreamPath string, body []byte, reqModel string, keyInfo interface{}, keyStr string, start time.Time, isOpenClaw, isHermes bool) {
	targetURL := strings.TrimRight(backend.URL, "/") + upstreamPath
	resp, err := h.doRequest(c, backend, upstreamPath, targetURL, body)
	if err != nil {
		if IsClientCanceled(err) {
			backend.RecordResult(ErrCanceled)
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": "upstream request failed"})
		return
	}
	defer resp.Body.Close()
	backend.RecordRequest(resp.StatusCode, time.Since(start).Milliseconds())
	for k, vv := range resp.Header {
		for _, v := range vv {
			c.Header(k, v)
		}
	}
	c.Status(resp.StatusCode)
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		h.streamResponse(c, resp, backend.Name, reqModel, keyInfo, keyStr, resp.StatusCode, start, isOpenClaw, isHermes, false, body)
	} else {
		h.bufferResponse(c, resp, backend.Name, reqModel, keyInfo, keyStr, resp.StatusCode, start, isOpenClaw, isHermes, false, body)
	}
}

// webSearchError ends the request with a 502-style Anthropic error, on either
// the SSE or JSON path depending on whether streaming already started.
func (h *Handler) webSearchError(c *gin.Context, sse *sseWriter, msg string, err error) {
	logger.Errorf("websearch: %s: %v", msg, err)
	if sse != nil {
		sse.writeError("api_error", msg)
		return
	}
	c.JSON(http.StatusBadGateway, gin.H{
		"type":  "error",
		"error": gin.H{"type": "api_error", "message": msg},
	})
}

// webSearchPassRawError forwards a first-round upstream error (already buffered
// in the loopError) to an Anthropic-protocol client. For SSE it emits an error
// event; otherwise it relays the status and body verbatim.
func (h *Handler) webSearchPassRawError(c *gin.Context, sse *sseWriter, lerr *loopError) {
	if sse != nil {
		sse.writeErrorRaw(lerr.RawBody)
		return
	}
	if lerr.ContentType != "" {
		c.Header("Content-Type", lerr.ContentType)
	}
	c.Status(lerr.Status)
	_, _ = c.Writer.Write(lerr.RawBody)
}
