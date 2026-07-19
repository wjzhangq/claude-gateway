package responses

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/wjzhangq/claude-gateway/internal/websearch"
)

// SSEWriter synthesizes an OpenAI Responses SSE stream from the fully-assembled
// loop result. Like the Anthropic web-search SSE writer, the agent loop runs to
// completion first and the whole event sequence is emitted at the end; during
// the loop's search phase Ping emits keep-alive comments so the client stays
// connected.
//
// Every event carries a monotonically increasing sequence_number and, where the
// real API does, a stable output_index / item_id. The top-level response object
// is shared (same id/model/created_at) across response.created, .in_progress and
// .completed.
type SSEWriter struct {
	w         io.Writer
	flush     func()
	seq       int
	started   bool
	id        string
	model     string
	createdAt int64
}

// NewSSEWriter returns a writer emitting to w. flush may be nil. Headers must be
// set by the caller before the first write.
func NewSSEWriter(w io.Writer, flush func(), model string) *SSEWriter {
	return &SSEWriter{
		w:         w,
		flush:     flush,
		id:        NewResponseID(),
		model:     model,
		createdAt: time.Now().Unix(),
	}
}

func (s *SSEWriter) emit(eventType string, payload map[string]interface{}) {
	payload["type"] = eventType
	payload["sequence_number"] = s.seq
	s.seq++
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", eventType, b)
	if s.flush != nil {
		s.flush()
	}
}

// responseEnvelope builds the top-level response object for created/in_progress/
// completed events.
func (s *SSEWriter) responseEnvelope(status string, output []OutputItem, usage *Usage, incomplete *IncompleteDetails) map[string]interface{} {
	env := map[string]interface{}{
		"id":         s.id,
		"object":     "response",
		"created_at": s.createdAt,
		"model":      s.model,
		"status":     status,
		"output":     output,
		"store":      false,
	}
	if usage != nil {
		env["usage"] = usage
	}
	if incomplete != nil {
		env["incomplete_details"] = incomplete
	}
	return env
}

// ensureStarted emits response.created + response.in_progress once.
func (s *SSEWriter) ensureStarted() {
	if s.started {
		return
	}
	s.started = true
	s.emit("response.created", map[string]interface{}{
		"response": s.responseEnvelope("in_progress", []OutputItem{}, nil, nil),
	})
	s.emit("response.in_progress", map[string]interface{}{
		"response": s.responseEnvelope("in_progress", []OutputItem{}, nil, nil),
	})
}

// Ping emits a keep-alive during a search. On the first call it lazily starts
// the stream so the client sees activity before the first item is ready.
func (s *SSEWriter) Ping() {
	s.ensureStarted()
	fmt.Fprint(s.w, ": keep-alive\n\n")
	if s.flush != nil {
		s.flush()
	}
}

// WriteResponse synthesizes the full event sequence for the assembled content
// and closes with response.completed.
func (s *SSEWriter) WriteResponse(blocks []websearch.ContentBlock, usage websearch.Usage, stopReason string) {
	s.ensureStarted()

	items := buildOutputItems(blocks)
	for i, item := range items {
		s.emitItem(i, item)
	}

	status := "completed"
	var incomplete *IncompleteDetails
	if stopReason == "max_tokens" {
		status = "incomplete"
		incomplete = &IncompleteDetails{Reason: "max_output_tokens"}
	}
	s.emit("response.completed", map[string]interface{}{
		"response": s.responseEnvelope(status, items, convertUsage(usage), incomplete),
	})
}

// emitItem emits the added → (type-specific deltas) → done events for one output
// item at output_index i.
func (s *SSEWriter) emitItem(i int, item OutputItem) {
	switch item.Type {
	case "message":
		// Announce the item with empty content, then stream each part.
		added := OutputItem{Type: "message", ID: item.ID, Status: "in_progress", Role: "assistant", Content: []OutputContentPart{}}
		s.emit("response.output_item.added", map[string]interface{}{"output_index": i, "item": added})
		for j, part := range item.Content {
			empty := OutputContentPart{Type: "output_text", Text: "", Annotations: []Annotation{}}
			s.emit("response.content_part.added", map[string]interface{}{
				"output_index": i, "content_index": j, "item_id": item.ID, "part": empty,
			})
			s.emit("response.output_text.delta", map[string]interface{}{
				"output_index": i, "content_index": j, "item_id": item.ID, "delta": part.Text,
			})
			s.emit("response.output_text.done", map[string]interface{}{
				"output_index": i, "content_index": j, "item_id": item.ID, "text": part.Text,
			})
			s.emit("response.content_part.done", map[string]interface{}{
				"output_index": i, "content_index": j, "item_id": item.ID, "part": part,
			})
		}
		s.emit("response.output_item.done", map[string]interface{}{"output_index": i, "item": item})

	case "web_search_call":
		added := OutputItem{Type: "web_search_call", ID: item.ID, Status: "in_progress", Action: item.Action}
		s.emit("response.output_item.added", map[string]interface{}{"output_index": i, "item": added})
		s.emit("response.web_search_call.in_progress", map[string]interface{}{"output_index": i, "item_id": item.ID})
		s.emit("response.web_search_call.searching", map[string]interface{}{"output_index": i, "item_id": item.ID})
		s.emit("response.web_search_call.completed", map[string]interface{}{"output_index": i, "item_id": item.ID})
		s.emit("response.output_item.done", map[string]interface{}{"output_index": i, "item": item})

	case "function_call":
		added := OutputItem{Type: "function_call", ID: item.ID, CallID: item.CallID, Name: item.Name, Arguments: "", Status: "in_progress"}
		s.emit("response.output_item.added", map[string]interface{}{"output_index": i, "item": added})
		s.emit("response.function_call_arguments.delta", map[string]interface{}{
			"output_index": i, "item_id": item.ID, "delta": item.Arguments,
		})
		s.emit("response.function_call_arguments.done", map[string]interface{}{
			"output_index": i, "item_id": item.ID, "arguments": item.Arguments,
		})
		s.emit("response.output_item.done", map[string]interface{}{"output_index": i, "item": item})
	}
}

// WriteError emits a Responses error event. Used when the loop fails after the
// stream has already started.
func (s *SSEWriter) WriteError(errType, msg string) {
	s.ensureStarted()
	s.emit("response.failed", map[string]interface{}{
		"response": map[string]interface{}{
			"id":     s.id,
			"object": "response",
			"status": "failed",
			"error":  RespError{Type: errType, Message: msg},
		},
	})
}
