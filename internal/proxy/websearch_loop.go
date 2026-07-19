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

	"github.com/wjzhangq/claude-gateway/internal/logger"
	"github.com/wjzhangq/claude-gateway/internal/websearch"
)

// maxWebSearchRounds hard-caps the number of upstream round-trips the loop makes
// regardless of the request's max_uses, bounding worst-case latency and cost if
// a model keeps calling web_search.
const maxWebSearchRounds = 12

// handleWithWebSearch runs the gateway web-search agent loop for a backend
// /v1/messages request that declared the web_search server tool. Each upstream
// round is a non-streaming call; the model's web_search tool_use calls are
// resolved against SearXNG and fed back until the model stops searching, a
// client tool is called, or the per-request cap is reached. The assembled
// content is then written to the client either as JSON (non-stream) or a
// synthesized Anthropic SSE stream (stream). Usage is aggregated across rounds
// and billed once through emitUsage.
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
	_, detectedMaxUses, allowed, blocked := websearch.DetectWebSearchTool(req.Tools)
	maxUses := detectedMaxUses
	if maxUses < 0 {
		maxUses = cfg.DefaultMaxUses
	}
	if maxUses <= 0 {
		maxUses = 1
	}

	// Rewrite for upstream: server tool -> client tool, normalize prior
	// gateway blocks, force non-streaming rounds.
	req.NormalizeHistory()
	req.RewriteTools()
	req.Stream = false

	// SSE setup: start the stream immediately so pings during searches keep the
	// client alive while the loop runs.
	var sse *sseWriter
	if clientWantsStream {
		sse = newSSEWriter(c)
	}

	targetURL := strings.TrimRight(backend.URL, "/") + upstreamPath
	var merged []websearch.ContentBlock
	var total websearch.Usage
	stopReason := "end_turn"
	respModel := reqModel
	uses := 0

	for round := 0; round < maxWebSearchRounds; round++ {
		roundBody, err := req.Encode()
		if err != nil {
			h.webSearchError(c, sse, "encode request failed", err)
			return
		}

		resp, err := h.doRequest(c, backend, upstreamPath, targetURL, roundBody)
		if err != nil {
			if IsClientCanceled(err) {
				backend.RecordResult(ErrCanceled)
				return
			}
			backend.RecordResult(ClassifyError(0, err))
			h.webSearchError(c, sse, "upstream request failed", err)
			return
		}

		parsed, rawStatus, ok := readMessagesResponse(resp)
		backend.RecordRequest(rawStatus, time.Since(start).Milliseconds())
		if isValidHealthModel(reqModel) {
			backend.RecordResultDetailed(ClassifyError(rawStatus, nil), 0, false)
		}
		if !ok || rawStatus >= 400 {
			// Upstream error: surface it verbatim on the first round, else stop
			// the loop with what we have.
			if round == 0 {
				h.webSearchPassUpstreamError(c, sse, resp, rawStatus)
				return
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

			if sse != nil {
				sse.ping()
			}
			results, searchErr := client.Search(c.Request.Context(), query, cfg.Language, allowed, blocked)
			if searchErr != nil {
				logger.Warnf("websearch: search %q failed: %v", query, searchErr)
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

	h.emitUsage(keyInfo, keyStr, backend.Name, reqModel, http.StatusOK,
		total.InputTokens, total.OutputTokens, time.Since(start),
		isOpenClaw, isHermes, false, c.Request.Header.Get("User-Agent"), c.ClientIP(), body)

	if sse != nil {
		sse.writeResponse(respModel, merged, total, stopReason)
	} else {
		writeJSONResponse(c, respModel, merged, total, stopReason)
	}
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
// response, transparently gunzipping. Returns the parsed message, the HTTP
// status, and whether decoding succeeded.
func readMessagesResponse(resp *http.Response) (*websearch.MessagesResponse, int, bool) {
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, false
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
		return nil, resp.StatusCode, false
	}
	return &parsed, resp.StatusCode, true
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

// webSearchPassUpstreamError forwards an upstream error response to the client.
// For SSE it emits an error event; otherwise it relays the status and body.
func (h *Handler) webSearchPassUpstreamError(c *gin.Context, sse *sseWriter, resp *http.Response, status int) {
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.Header.Get("Content-Encoding") == "gzip" {
		if r, e := gzip.NewReader(bytes.NewReader(raw)); e == nil {
			if dec, e2 := io.ReadAll(r); e2 == nil {
				raw = dec
			}
		}
	}
	if sse != nil {
		sse.writeErrorRaw(raw)
		return
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		c.Header("Content-Type", ct)
	}
	c.Status(status)
	_, _ = c.Writer.Write(raw)
}
