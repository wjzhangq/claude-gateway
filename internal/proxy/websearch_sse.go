package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/wjzhangq/claude-gateway/internal/websearch"
)

// writeJSONResponse emits a complete Anthropic MessagesResponse as JSON for a
// non-streaming client. The message id is synthesized; the client only needs a
// well-formed response with the assembled content, stop_reason and aggregated
// usage.
func writeJSONResponse(c *gin.Context, model string, content []websearch.ContentBlock, usage websearch.Usage, stopReason string) {
	resp := websearch.MessagesResponse{
		ID:         "msg_" + websearchStripPrefix(websearch.NewServerToolID()),
		Type:       "message",
		Role:       "assistant",
		Model:      model,
		Content:    content,
		StopReason: stopReason,
		Usage:      usage,
	}
	c.Header("Content-Type", "application/json")
	c.Status(http.StatusOK)
	_ = json.NewEncoder(c.Writer).Encode(resp)
}

// websearchStripPrefix drops the "srvtoolu_" prefix from a generated id so it
// can be reused as the random suffix of a message id.
func websearchStripPrefix(id string) string {
	const p = "srvtoolu_"
	if len(id) > len(p) {
		return id[len(p):]
	}
	return id
}

// sseWriter synthesizes an Anthropic Messages SSE stream for the web-search
// loop: message_start, then per-block content_block_start/delta/stop, then
// message_delta (with real stop_reason + aggregated usage) and message_stop.
// It also emits ping events to keep the client alive during searches.
type sseWriter struct {
	c       *gin.Context
	flusher http.Flusher
	started bool
	msgID   string
}

// newSSEWriter prepares the response for SSE and returns a writer. Headers are
// set here so the client sees the stream open immediately.
func newSSEWriter(c *gin.Context) *sseWriter {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	fl, _ := c.Writer.(http.Flusher)
	return &sseWriter{
		c:       c,
		flusher: fl,
		msgID:   "msg_" + websearchStripPrefix(websearch.NewServerToolID()),
	}
}

func (s *sseWriter) emit(event string, payload interface{}) {
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	fmt.Fprintf(s.c.Writer, "event: %s\ndata: %s\n\n", event, b)
	if s.flusher != nil {
		s.flusher.Flush()
	}
}

// ping keeps the connection alive during a search. It also lazily emits
// message_start on the first call so the client sees activity before the first
// content block is ready.
func (s *sseWriter) ping() {
	s.ensureStarted()
	fmt.Fprintf(s.c.Writer, "event: ping\ndata: {\"type\": \"ping\"}\n\n")
	if s.flusher != nil {
		s.flusher.Flush()
	}
}

func (s *sseWriter) ensureStarted() {
	if s.started {
		return
	}
	s.started = true
	s.emit("message_start", map[string]interface{}{
		"type": "message_start",
		"message": map[string]interface{}{
			"id":            s.msgID,
			"type":          "message",
			"role":          "assistant",
			"content":       []interface{}{},
			"model":         "",
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         map[string]interface{}{"input_tokens": 0, "output_tokens": 0},
		},
	})
}

// writeResponse streams the fully-assembled content as a synthesized SSE
// sequence, then closes with message_delta + message_stop.
func (s *sseWriter) writeResponse(model string, content []websearch.ContentBlock, usage websearch.Usage, stopReason string) {
	s.ensureStarted()
	for i, block := range content {
		s.emitBlock(i, block)
	}
	s.emit("message_delta", map[string]interface{}{
		"type": "message_delta",
		"delta": map[string]interface{}{
			"stop_reason":   stopReason,
			"stop_sequence": nil,
		},
		"usage": usage,
	})
	s.emit("message_stop", map[string]interface{}{"type": "message_stop"})
}

// emitBlock emits one content block as start/delta/stop. Text blocks stream
// their text as a single text_delta; tool_use / server_tool_use stream their
// input as one input_json_delta; result/other blocks are sent whole with no
// delta (matching Anthropic, where web_search_tool_result has no delta).
func (s *sseWriter) emitBlock(index int, block websearch.ContentBlock) {
	switch block.Type {
	case "text":
		s.emit("content_block_start", map[string]interface{}{
			"type":          "content_block_start",
			"index":         index,
			"content_block": map[string]interface{}{"type": "text", "text": ""},
		})
		s.emit("content_block_delta", map[string]interface{}{
			"type":  "content_block_delta",
			"index": index,
			"delta": map[string]interface{}{"type": "text_delta", "text": block.Text},
		})
		s.emit("content_block_stop", map[string]interface{}{"type": "content_block_stop", "index": index})

	case "tool_use", "server_tool_use":
		start := map[string]interface{}{"type": block.Type, "id": block.ID, "name": block.Name, "input": map[string]interface{}{}}
		s.emit("content_block_start", map[string]interface{}{
			"type":          "content_block_start",
			"index":         index,
			"content_block": start,
		})
		partial := ""
		if len(block.Input) > 0 {
			partial = string(block.Input)
		}
		s.emit("content_block_delta", map[string]interface{}{
			"type":  "content_block_delta",
			"index": index,
			"delta": map[string]interface{}{"type": "input_json_delta", "partial_json": partial},
		})
		s.emit("content_block_stop", map[string]interface{}{"type": "content_block_stop", "index": index})

	default:
		// web_search_tool_result and any passthrough block: emit whole, no delta.
		s.emit("content_block_start", map[string]interface{}{
			"type":          "content_block_start",
			"index":         index,
			"content_block": block,
		})
		s.emit("content_block_stop", map[string]interface{}{"type": "content_block_stop", "index": index})
	}
}

// writeError emits an SSE error event with a synthesized Anthropic error shape.
func (s *sseWriter) writeError(errType, msg string) {
	s.emit("error", map[string]interface{}{
		"type":  "error",
		"error": map[string]interface{}{"type": errType, "message": msg},
	})
}

// writeErrorRaw emits an SSE error event carrying an upstream error body
// verbatim when it is valid JSON, else wraps it as a message.
func (s *sseWriter) writeErrorRaw(raw []byte) {
	if json.Valid(raw) {
		fmt.Fprintf(s.c.Writer, "event: error\ndata: %s\n\n", raw)
		if s.flusher != nil {
			s.flusher.Flush()
		}
		return
	}
	s.writeError("api_error", string(raw))
}
