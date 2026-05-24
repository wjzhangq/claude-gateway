package awsproxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	"github.com/wjzhangq/claude-gateway/config"
	"github.com/wjzhangq/claude-gateway/internal/auth"
	"github.com/wjzhangq/claude-gateway/internal/logger"
	"github.com/wjzhangq/claude-gateway/internal/middleware"
	"github.com/wjzhangq/claude-gateway/internal/stats"
)

// Handler forwards requests to AWS Bedrock.
type Handler struct {
	client     *BedrockClient
	collector  *stats.Collector
	keyStore   *auth.KeyStore
	mu         sync.RWMutex
	config     *config.AWSConfig
	rootConfig *config.Config // full config for user_daily_limits lookup
}

// NewHandler creates a new AWS proxy handler.
func NewHandler(client *BedrockClient, collector *stats.Collector, ks *auth.KeyStore, cfg *config.AWSConfig) *Handler {
	return &Handler{
		client:    client,
		collector: collector,
		keyStore:  ks,
		config:    cfg,
	}
}

// SetRootConfig sets the full config on the handler (call after NewHandler).
func (h *Handler) SetRootConfig(cfg *config.Config) {
	h.mu.Lock()
	h.rootConfig = cfg
	h.mu.Unlock()
}

// UpdateConfig hot-swaps the AWS config (used on SIGHUP reload).
func (h *Handler) UpdateConfig(cfg *config.AWSConfig) {
	h.mu.Lock()
	h.config = cfg
	h.mu.Unlock()
}

func (h *Handler) awsCfg() *config.AWSConfig {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.config
}

// Passthrough routes /v1/* requests to the appropriate handler.
func (h *Handler) Passthrough(c *gin.Context) {
	path := "/v1" + c.Param("path")
	switch {
	case strings.HasSuffix(path, "/chat/completions"):
		h.ChatCompletions(c)
	case strings.HasSuffix(path, "/messages"):
		h.Messages(c)
	case strings.HasSuffix(path, "/models"):
		h.Models(c)
	default:
		c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("AWS channel does not support path %s", path)})
	}
}

// Models handles GET /v1/models — returns model_replace keys plus any extra models.
func (h *Handler) Models(c *gin.Context) {
	cfg := h.awsCfg()
	models := ListAvailableModels(cfg.ModelReplace)

	now := time.Now().Unix()
	data := make([]gin.H, 0, len(models))
	for _, m := range models {
		data = append(data, gin.H{
			"id":       m,
			"object":   "model",
			"created":  now,
			"owned_by": "aws-bedrock",
		})
	}

	// Append extra models (e.g. public providers) if set in context
	if extra, ok := c.Get("extra_models"); ok {
		if extraModels, ok := extra.([]gin.H); ok {
			data = append(data, extraModels...)
		}
	}

	c.JSON(http.StatusOK, gin.H{"object": "list", "data": data})
}

// ─────────────────────────────────────────────
//  /v1/messages  (Anthropic Messages API)
// ─────────────────────────────────────────────

// isBlockedClient returns true if the User-Agent is from a client that is
// officially banned from using this service via AWS Bedrock.
func isBlockedClient(userAgent string) bool {
	ua := strings.ToLower(userAgent)
	return strings.Contains(ua, "openclaw")
}

// checkAWSDailyLimit checks if the user has exceeded the AWS daily spending limit.
// Returns true if the request should be blocked (limit exceeded).
func (h *Handler) checkAWSDailyLimit(c *gin.Context) bool {
	keyInfo, _ := extractKeyInfo(c)
	if keyInfo == nil {
		return false
	}

	cfg := h.awsCfg()

	// Effective daily limit resolution (highest priority first):
	// 1. If the user's itcode appears in config.user_daily_limits with aws_daily_usd > 0,
	//    use that value directly — it overrides both the global cap and the DB quota.
	// 2. Otherwise fall back to min(global AWSDailyMax, per-user AWSDailyQuotaUSD),
	//    treating 0 as "no limit" for each.
	var effectiveLimit float64
	h.mu.RLock()
	root := h.rootConfig
	h.mu.RUnlock()
	if root != nil {
		if override := root.LookupUserDailyLimit(keyInfo.Itcode); override != nil && override.AWSDailyUSD > 0 {
			effectiveLimit = override.AWSDailyUSD
		}
	}
	if effectiveLimit == 0 {
		globalMax := cfg.AWSDailyMax
		userMax := keyInfo.AWSDailyQuotaUSD
		switch {
		case globalMax > 0 && userMax > 0:
			if globalMax < userMax {
				effectiveLimit = globalMax
			} else {
				effectiveLimit = userMax
			}
		case globalMax > 0:
			effectiveLimit = globalMax
		case userMax > 0:
			effectiveLimit = userMax
		}
	}

	if effectiveLimit > 0 {
		todayCost := h.keyStore.GetAWSDailyCost(keyInfo.UserID)
		if todayCost >= effectiveLimit {
			logger.Warnf("daily AWS limit exceeded: user_id=%d today=%.4f limit=%.4f", keyInfo.UserID, todayCost, effectiveLimit)
			c.JSON(http.StatusTooManyRequests, gin.H{
				"type": "error",
				"error": gin.H{
					"type":    "rate_limit_error",
					"message": "Daily AWS spending limit exceeded. Please try again tomorrow.",
				},
			})
			return true
		}
	}
	return false
}

// Messages handles POST /v1/messages.
func (h *Handler) Messages(c *gin.Context) {
	if isBlockedClient(c.Request.Header.Get("User-Agent")) {
		c.JSON(http.StatusForbidden, gin.H{
			"error": gin.H{
				"type":    "permission_denied",
				"message": "Access denied: your client (OpenClaw) is officially prohibited from using this service. Please use an authorized client.",
			},
		})
		return
	}

	if h.checkAWSDailyLimit(c) {
		return
	}

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "read request body failed"})
		return
	}

	var req struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON body"})
		return
	}

	cfg := h.awsCfg()
	bedrockModel, err := ResolveModel(req.Model, cfg.ModelReplace, cfg.ModelDefault)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Prepare body: remove model, add anthropic_version
	bedrockBody := prepareAnthropicBody(body)

	keyInfo, keyStr := extractKeyInfo(c)
	start := time.Now()

	// Set log context for the request logger middleware
	c.Set("log_provider", "aws")
	c.Set("log_backend_name", bedrockModel)
	c.Set("log_model", req.Model)

	if req.Stream {
		h.streamMessages(c, bedrockBody, bedrockModel, req.Model, keyInfo, keyStr, start)
	} else {
		h.syncMessages(c, bedrockBody, bedrockModel, req.Model, keyInfo, keyStr, start)
	}
}

func (h *Handler) syncMessages(c *gin.Context, body []byte, bedrockModel, reqModel string,
	keyInfo *auth.KeyInfo, keyStr string, start time.Time) {

	respBytes, err := h.client.InvokeModel(c.Request.Context(), bedrockModel, body)
	if err != nil {
		logBedrockError("bedrock invoke failed", err, body, bedrockModel, reqModel,
			keyInfo, c.Request.Header.Get("User-Agent"), false)
		c.JSON(http.StatusBadGateway, gin.H{"error": "bedrock request failed"})
		return
	}

	in, out, cacheRead, cacheWrite := parseAnthropicUsage(respBytes)
	statusCode := http.StatusOK

	c.Header("Content-Type", "application/json")
	c.Status(statusCode)
	c.Writer.Write(respBytes)

	h.emitUsage(keyInfo, keyStr, reqModel, bedrockModel, statusCode, in, out, cacheRead, cacheWrite,
		time.Since(start), c.Request.Header.Get("User-Agent"))
}

func (h *Handler) streamMessages(c *gin.Context, body []byte, bedrockModel, reqModel string,
	keyInfo *auth.KeyInfo, keyStr string, start time.Time) {

	out, err := h.client.InvokeModelStream(c.Request.Context(), bedrockModel, body)
	if err != nil {
		logBedrockError("bedrock stream invoke failed", err, body, bedrockModel, reqModel,
			keyInfo, c.Request.Header.Get("User-Agent"), true)
		c.JSON(http.StatusBadGateway, gin.H{"error": "bedrock stream request failed"})
		return
	}
	defer out.GetStream().Close()

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")

	flusher, canFlush := c.Writer.(http.Flusher)

	var inputTokens, outputTokens, cacheRead, cacheWrite int

	for event := range out.GetStream().Events() {
		switch v := event.(type) {
		case *types.ResponseStreamMemberChunk:
			chunk := v.Value.Bytes
			// Extract token counts from known event types
			in2, out2, cr2, cw2 := parseAnthropicUsage(chunk)
			if in2 > 0 {
				inputTokens = in2
			}
			if out2 > 0 {
				outputTokens = out2
			}
			if cr2 > 0 {
				cacheRead = cr2
			}
			if cw2 > 0 {
				cacheWrite = cw2
			}

			c.Writer.WriteString("data: ")
			c.Writer.Write(chunk)
			c.Writer.WriteString("\n\n")
			if canFlush {
				flusher.Flush()
			}
		}
	}

	h.emitUsage(keyInfo, keyStr, reqModel, bedrockModel, http.StatusOK,
		inputTokens, outputTokens, cacheRead, cacheWrite,
		time.Since(start), c.Request.Header.Get("User-Agent"))
}

// ─────────────────────────────────────────────
//  /v1/chat/completions  (OpenAI format)
// ─────────────────────────────────────────────

// ChatCompletions handles POST /v1/chat/completions.
func (h *Handler) ChatCompletions(c *gin.Context) {
	if isBlockedClient(c.Request.Header.Get("User-Agent")) {
		c.JSON(http.StatusForbidden, gin.H{
			"error": gin.H{
				"type":    "permission_denied",
				"message": "Access denied: your client (OpenClaw) is officially prohibited from using this service. Please use an authorized client.",
			},
		})
		return
	}

	if h.checkAWSDailyLimit(c) {
		return
	}

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "read request body failed"})
		return
	}

	var oaiReq openAIChatRequest
	if err := json.Unmarshal(body, &oaiReq); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON body"})
		return
	}

	cfg := h.awsCfg()
	bedrockModel, err := ResolveModel(oaiReq.Model, cfg.ModelReplace, cfg.ModelDefault)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Convert OpenAI format to Anthropic format
	anthropicReq := convertOAIToAnthropic(oaiReq)
	bedrockBody, err := json.Marshal(anthropicReq)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "marshal failed"})
		return
	}

	keyInfo, keyStr := extractKeyInfo(c)
	start := time.Now()

	// Set log context for the request logger middleware
	c.Set("log_provider", "aws")
	c.Set("log_backend_name", bedrockModel)
	c.Set("log_model", oaiReq.Model)

	if oaiReq.Stream {
		h.streamChatCompletions(c, bedrockBody, bedrockModel, oaiReq.Model, keyInfo, keyStr, start)
	} else {
		h.syncChatCompletions(c, bedrockBody, bedrockModel, oaiReq.Model, keyInfo, keyStr, start)
	}
}

func (h *Handler) syncChatCompletions(c *gin.Context, body []byte, bedrockModel, reqModel string,
	keyInfo *auth.KeyInfo, keyStr string, start time.Time) {

	respBytes, err := h.client.InvokeModel(c.Request.Context(), bedrockModel, body)
	if err != nil {
		logBedrockError("bedrock invoke failed", err, body, bedrockModel, reqModel,
			keyInfo, c.Request.Header.Get("User-Agent"), false)
		c.JSON(http.StatusBadGateway, gin.H{"error": "bedrock request failed"})
		return
	}

	var anthropicResp anthropicMessageResponse
	if err := json.Unmarshal(respBytes, &anthropicResp); err != nil {
		logger.Errorf("parse bedrock response: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "parse response failed"})
		return
	}

	oaiResp := convertAnthropicToOAI(anthropicResp, reqModel)

	in := anthropicResp.Usage.InputTokens
	out := anthropicResp.Usage.OutputTokens
	cacheRead := anthropicResp.Usage.CacheReadInputTokens
	cacheWrite := anthropicResp.Usage.CacheCreationInputTokens

	c.JSON(http.StatusOK, oaiResp)

	h.emitUsage(keyInfo, keyStr, reqModel, bedrockModel, http.StatusOK,
		in, out, cacheRead, cacheWrite,
		time.Since(start), c.Request.Header.Get("User-Agent"))
}

func (h *Handler) streamChatCompletions(c *gin.Context, body []byte, bedrockModel, reqModel string,
	keyInfo *auth.KeyInfo, keyStr string, start time.Time) {

	out, err := h.client.InvokeModelStream(c.Request.Context(), bedrockModel, body)
	if err != nil {
		logBedrockError("bedrock stream invoke failed", err, body, bedrockModel, reqModel,
			keyInfo, c.Request.Header.Get("User-Agent"), true)
		c.JSON(http.StatusBadGateway, gin.H{"error": "bedrock stream request failed"})
		return
	}
	defer out.GetStream().Close()

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")

	flusher, canFlush := c.Writer.(http.Flusher)

	chatID := fmt.Sprintf("chatcmpl-aws-%d", time.Now().UnixNano())
	created := time.Now().Unix()

	var inputTokens, outputTokens, cacheRead, cacheWrite int

	for event := range out.GetStream().Events() {
		switch v := event.(type) {
		case *types.ResponseStreamMemberChunk:
			chunk := v.Value.Bytes

			// Accumulate token counts
			in2, out2, cr2, cw2 := parseAnthropicUsage(chunk)
			if in2 > 0 {
				inputTokens = in2
			}
			if out2 > 0 {
				outputTokens = out2
			}
			if cr2 > 0 {
				cacheRead = cr2
			}
			if cw2 > 0 {
				cacheWrite = cw2
			}

			// Convert Anthropic SSE chunk to OpenAI SSE chunk
			oaiChunks := convertAnthropicStreamChunk(chunk, chatID, reqModel, created)
			for _, oc := range oaiChunks {
				if data, err := json.Marshal(oc); err == nil {
					c.Writer.WriteString("data: ")
					c.Writer.Write(data)
					c.Writer.WriteString("\n\n")
					if canFlush {
						flusher.Flush()
					}
				}
			}
		}
	}

	// Send [DONE]
	c.Writer.WriteString("data: [DONE]\n\n")
	if canFlush {
		flusher.Flush()
	}

	h.emitUsage(keyInfo, keyStr, reqModel, bedrockModel, http.StatusOK,
		inputTokens, outputTokens, cacheRead, cacheWrite,
		time.Since(start), c.Request.Header.Get("User-Agent"))
}

// ─────────────────────────────────────────────
//  Format types & conversion helpers
// ─────────────────────────────────────────────

type openAIChatRequest struct {
	Model       string        `json:"model"`
	Messages    []openAIMsg   `json:"messages"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Temperature *float64      `json:"temperature,omitempty"`
	TopP        *float64      `json:"top_p,omitempty"`
	Stream      bool          `json:"stream,omitempty"`
	Stop        []string      `json:"stop,omitempty"`
	System      string        `json:"system,omitempty"`
}

type openAIMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicRequest struct {
	AnthropicVersion string      `json:"anthropic_version"`
	System           string      `json:"system,omitempty"`
	Messages         []openAIMsg `json:"messages"`
	MaxTokens        int         `json:"max_tokens"`
	Temperature      *float64    `json:"temperature,omitempty"`
	TopP             *float64    `json:"top_p,omitempty"`
	Stop             []string    `json:"stop_sequences,omitempty"`
}

type anthropicMessageResponse struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Role       string `json:"role"`
	Content    []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

// convertOAIToAnthropic converts an OpenAI chat completions request to Anthropic Messages format.
func convertOAIToAnthropic(req openAIChatRequest) anthropicRequest {
	ar := anthropicRequest{
		AnthropicVersion: "bedrock-2023-05-31",
		MaxTokens:        req.MaxTokens,
		Temperature:      req.Temperature,
		TopP:             req.TopP,
		Stop:             req.Stop,
		System:           req.System,
	}
	if ar.MaxTokens <= 0 {
		ar.MaxTokens = 4096 // default
	}

	var msgs []openAIMsg
	for _, m := range req.Messages {
		if m.Role == "system" {
			if ar.System == "" {
				ar.System = m.Content
			}
			continue
		}
		msgs = append(msgs, m)
	}
	ar.Messages = msgs
	return ar
}

// convertAnthropicToOAI converts an Anthropic response to OpenAI chat completion format.
func convertAnthropicToOAI(resp anthropicMessageResponse, model string) gin.H {
	text := ""
	for _, block := range resp.Content {
		if block.Type == "text" {
			text += block.Text
		}
	}

	finishReason := "stop"
	if resp.StopReason == "max_tokens" {
		finishReason = "length"
	}

	return gin.H{
		"id":      resp.ID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []gin.H{
			{
				"index": 0,
				"message": gin.H{
					"role":    "assistant",
					"content": text,
				},
				"finish_reason": finishReason,
			},
		},
		"usage": gin.H{
			"prompt_tokens":     resp.Usage.InputTokens,
			"completion_tokens": resp.Usage.OutputTokens,
			"total_tokens":      resp.Usage.InputTokens + resp.Usage.OutputTokens,
		},
	}
}

// convertAnthropicStreamChunk converts a raw Anthropic SSE chunk to OpenAI SSE chunks.
func convertAnthropicStreamChunk(chunk []byte, id, model string, created int64) []gin.H {
	var evt struct {
		Type  string `json:"type"`
		Index int    `json:"index"`
		Delta struct {
			Type       string `json:"type"`
			Text       string `json:"text"`
			StopReason string `json:"stop_reason"`
		} `json:"delta"`
	}
	if err := json.Unmarshal(chunk, &evt); err != nil {
		return nil
	}

	switch evt.Type {
	case "content_block_delta":
		if evt.Delta.Type == "text_delta" && evt.Delta.Text != "" {
			return []gin.H{{
				"id":      id,
				"object":  "chat.completion.chunk",
				"created": created,
				"model":   model,
				"choices": []gin.H{{
					"index":         0,
					"delta":         gin.H{"content": evt.Delta.Text},
					"finish_reason": nil,
				}},
			}}
		}
	case "message_delta":
		finishReason := "stop"
		if evt.Delta.StopReason == "max_tokens" {
			finishReason = "length"
		}
		return []gin.H{{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model,
			"choices": []gin.H{{
				"index":         0,
				"delta":         gin.H{},
				"finish_reason": finishReason,
			}},
		}}
	}
	return nil
}

// stripThinkingBlocks removes all thinking and redacted_thinking content blocks
// from messages. Bedrock rejects thinking blocks with signatures generated by
// a different model/inference profile, and simply deleting the signature field
// causes Bedrock to reject the block for a missing required field. The safest
// approach is to strip these blocks entirely from conversation history.
func stripThinkingBlocks(messages []interface{}) []interface{} {
	for i, msg := range messages {
		msgMap, ok := msg.(map[string]interface{})
		if !ok {
			continue
		}
		blocks, ok := msgMap["content"].([]interface{})
		if !ok {
			continue
		}
		hasThinking := false
		for _, block := range blocks {
			bm, ok := block.(map[string]interface{})
			if !ok {
				continue
			}
			typ, _ := bm["type"].(string)
			if typ == "thinking" || typ == "redacted_thinking" {
				hasThinking = true
				break
			}
		}
		if !hasThinking {
			continue
		}
		var filtered []interface{}
		for _, block := range blocks {
			bm, ok := block.(map[string]interface{})
			if !ok {
				filtered = append(filtered, block)
				continue
			}
			typ, _ := bm["type"].(string)
			if typ == "thinking" || typ == "redacted_thinking" {
				continue
			}
			filtered = append(filtered, block)
		}
		if len(filtered) == 0 {
			filtered = []interface{}{map[string]interface{}{"type": "text", "text": " "}}
		}
		msgMap["content"] = filtered
		messages[i] = msgMap
	}
	return messages
}

// stripCacheControlScope removes the "scope" field from any cache_control object
// found anywhere in a JSON value. Bedrock does not support cache_control.scope
// (it is an Anthropic-only extension added after bedrock-2023-05-31).
func stripCacheControlScope(v interface{}) interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		if cc, ok := val["cache_control"]; ok {
			if ccMap, ok := cc.(map[string]interface{}); ok {
				delete(ccMap, "scope")
			}
		}
		for k, child := range val {
			val[k] = stripCacheControlScope(child)
		}
	case []interface{}:
		for i, item := range val {
			val[i] = stripCacheControlScope(item)
		}
	}
	return v
}

// stripEmptyTextBlocks removes empty text content blocks from messages.
// Bedrock rejects requests with {"type":"text","text":""}.
// When content is an array, filter out blocks where type=="text" and text is
// empty. If all blocks are removed, replace with a single whitespace text block
// so the message remains valid.
func stripEmptyTextBlocks(messages []interface{}) []interface{} {
	for i, msg := range messages {
		msgMap, ok := msg.(map[string]interface{})
		if !ok {
			continue
		}
		content, ok := msgMap["content"]
		if !ok {
			continue
		}
		// content can be a string or an array of content blocks
		blocks, ok := content.([]interface{})
		if !ok {
			// string content — if empty, replace with whitespace
			if s, ok := content.(string); ok && strings.TrimSpace(s) == "" {
				msgMap["content"] = " "
				messages[i] = msgMap
			}
			continue
		}
		var filtered []interface{}
		for _, block := range blocks {
			bm, ok := block.(map[string]interface{})
			if !ok {
				filtered = append(filtered, block)
				continue
			}
			typ, _ := bm["type"].(string)
			if typ == "text" {
				text, _ := bm["text"].(string)
				if strings.TrimSpace(text) == "" {
					continue // drop empty text block
				}
			}
			filtered = append(filtered, block)
		}
		if len(filtered) == 0 {
			// All blocks were empty; keep at least one to satisfy API requirement
			filtered = []interface{}{map[string]interface{}{"type": "text", "text": " "}}
		}
		msgMap["content"] = filtered
		messages[i] = msgMap
	}
	return messages
}


// prepareAnthropicBody sets anthropic_version to "bedrock-2023-05-31" and removes
// fields that Bedrock does not accept: "model" and "stream".
// It also ensures max_tokens > thinking.budget_tokens when extended thinking is used,
// and strips cache_control.scope which Bedrock does not support.
func prepareAnthropicBody(body []byte) []byte {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}
	delete(m, "model")
	delete(m, "stream")            // Bedrock rejects this field: "Extra inputs are not permitted"
	delete(m, "context_management") // Bedrock does not support context_management (added by Claude Code CLI)
	delete(m, "metadata")          // Bedrock does not support metadata field
	delete(m, "output_config")     // Bedrock does not support output_config (e.g. effort, format fields)
	m["anthropic_version"] = json.RawMessage(`"bedrock-2023-05-31"`)

	// Filter out non-standard tool types that Bedrock doesn't support
	// (e.g. web_search_20250305, code_execution). Only keep tools with
	// type "custom" or with an "input_schema" (standard function-call tools).
	if toolsRaw, ok := m["tools"]; ok {
		var tools []map[string]interface{}
		if err := json.Unmarshal(toolsRaw, &tools); err == nil {
			var filtered []map[string]interface{}
			for _, tool := range tools {
				toolType, _ := tool["type"].(string)
				// Standard tools have type="" (omitted), "custom", or have input_schema
				_, hasInputSchema := tool["input_schema"]
				if toolType == "" || toolType == "custom" || hasInputSchema {
					filtered = append(filtered, tool)
				} else {
					logger.Warnf("prepareAnthropicBody: stripping unsupported tool type %q (name=%v)", toolType, tool["name"])
				}
			}
			if len(filtered) == 0 {
				delete(m, "tools")
			} else if len(filtered) != len(tools) {
				if b, err := json.Marshal(filtered); err == nil {
					m["tools"] = b
				}
			}
		}
	}

	// Strip cache_control.scope from system/messages — Bedrock does not support it.
	// Also strip empty text content blocks from messages — Bedrock rejects them.
	for _, key := range []string{"system", "messages"} {
		if raw, ok := m[key]; ok {
			var parsed interface{}
			if err := json.Unmarshal(raw, &parsed); err == nil {
				cleaned := stripCacheControlScope(parsed)
				if key == "messages" {
					if msgs, ok := cleaned.([]interface{}); ok {
						cleaned = stripThinkingBlocks(stripEmptyTextBlocks(msgs))
					}
				}
				if b, err := json.Marshal(cleaned); err == nil {
					m[key] = b
				}
			}
		}
	}

	// Enforce max_tokens > thinking.budget_tokens (Bedrock requirement).
	if thinkingRaw, ok := m["thinking"]; ok {
		var thinking struct {
			BudgetTokens int `json:"budget_tokens"`
		}
		if err := json.Unmarshal(thinkingRaw, &thinking); err == nil && thinking.BudgetTokens > 0 {
			var maxTokens int
			if maxRaw, ok := m["max_tokens"]; ok {
				_ = json.Unmarshal(maxRaw, &maxTokens)
			}
			if maxTokens <= thinking.BudgetTokens {
				// Set max_tokens to budget_tokens + 1024 to leave headroom for output.
				newMax := thinking.BudgetTokens + 1024
				if raw, err := json.Marshal(newMax); err == nil {
					m["max_tokens"] = raw
					logger.Warnf("prepareAnthropicBody: max_tokens (%d) <= thinking.budget_tokens (%d), adjusted to %d",
						maxTokens, thinking.BudgetTokens, newMax)
				}
			}
		}
	}

	if out, err := json.Marshal(m); err == nil {
		return out
	}
	return body
}

// parseAnthropicUsage extracts token counts from any Anthropic JSON payload.
func parseAnthropicUsage(data []byte) (input, output, cacheRead, cacheWrite int) {
	var r struct {
		Usage struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
		Message struct {
			Usage struct {
				InputTokens              int `json:"input_tokens"`
				OutputTokens             int `json:"output_tokens"`
				CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
				CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			} `json:"usage"`
		} `json:"message"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return
	}
	// message_start event nests usage under "message"
	input = r.Usage.InputTokens + r.Message.Usage.InputTokens
	output = r.Usage.OutputTokens + r.Message.Usage.OutputTokens
	cacheRead = r.Usage.CacheReadInputTokens + r.Message.Usage.CacheReadInputTokens
	cacheWrite = r.Usage.CacheCreationInputTokens + r.Message.Usage.CacheCreationInputTokens
	return
}

// extractKeyInfo returns the key info and raw key string from context.
func extractKeyInfo(c *gin.Context) (*auth.KeyInfo, string) {
	raw, _ := c.Get(middleware.CtxKeyInfo)
	info, _ := raw.(*auth.KeyInfo)

	keyStr := c.GetHeader("Authorization")
	keyStr = strings.TrimPrefix(keyStr, "Bearer ")
	keyStr = strings.TrimPrefix(keyStr, "bearer ")
	if keyStr == "" {
		keyStr = c.GetHeader("x-api-key")
	}
	return info, keyStr
}

// emitUsage records an AWS usage event to the collector.
func (h *Handler) emitUsage(keyInfo *auth.KeyInfo, keyStr, reqModel, bedrockModel string,
	statusCode, inputTokens, outputTokens, cacheRead, cacheWrite int,
	latency time.Duration, userAgent string) {

	if h.collector == nil || keyInfo == nil {
		return
	}
	cfg := h.awsCfg()
	cost := AWSCostUSD(reqModel, inputTokens, outputTokens, cacheRead, cacheWrite, cfg.ModelPricing)
	ua := parseUA(userAgent)

	h.collector.Emit(stats.Record{
		UserID:           keyInfo.UserID,
		GroupID:          keyInfo.GroupID,
		APIKeyID:         keyInfo.KeyID,
		KeyStr:           keyStr,
		Provider:         "aws",
		Model:            reqModel,
		BackendName:      bedrockModel,
		InputTokens:      inputTokens,
		OutputTokens:     outputTokens,
		TotalTokens:      inputTokens + outputTokens,
		CacheReadTokens:  cacheRead,
		CacheWriteTokens: cacheWrite,
		CostUSD:          cost,
		StatusCode:       statusCode,
		Latency:          latency,
		UA:               ua,
	})

	// Accumulate AWS daily cost for per-user quota tracking
	if cost > 0 && statusCode < 400 {
		h.keyStore.AddAWSDailyCost(keyInfo.UserID, cost)
	}
}

// parseUA extracts the UA product name (≤12 chars, lowercase).
func parseUA(userAgent string) string {
	if userAgent == "" {
		return ""
	}
	ua := userAgent
	if idx := strings.Index(ua, "/"); idx > 0 {
		ua = ua[:idx]
	}
	if len(ua) > 12 {
		ua = ua[:12]
	}
	return strings.ToLower(ua)
}

// logBedrockError logs a failed Bedrock request with full context to the
// daily error log file for post-mortem analysis.
func logBedrockError(msg string, err error, reqBody []byte, bedrockModel, reqModel string,
	keyInfo *auth.KeyInfo, userAgent string, stream bool) {

	fields := logrus.Fields{
		"error":         err.Error(),
		"bedrock_model": bedrockModel,
		"req_model":     reqModel,
		"stream":        stream,
		"user_agent":    userAgent,
	}
	if keyInfo != nil {
		fields["user_id"] = keyInfo.UserID
		fields["itcode"] = keyInfo.Itcode
		fields["key_id"] = keyInfo.KeyID
	}
	// Truncate body to 4KB to avoid huge log entries
	body := string(reqBody)
	if len(body) > 4096 {
		body = body[:4096] + "...(truncated)"
	}
	fields["request_body"] = body

	logger.LogErrorRequest(msg, fields, "request_body")
}
