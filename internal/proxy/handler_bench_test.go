package proxy

import (
	"bytes"
	"fmt"
	"testing"
)

// buildSSEStream builds a synthetic Anthropic-style SSE stream with the given
// number of content_block_delta events, framed by message_start and
// message_delta events that carry the usage numbers.
func buildSSEStream(events int) []byte {
	var b bytes.Buffer
	b.WriteString("event: message_start\n")
	b.WriteString(`data: {"type":"message_start","message":{"usage":{"input_tokens":1200,"cache_read_input_tokens":800}}}`)
	b.WriteString("\n\n")
	for i := 0; i < events; i++ {
		b.WriteString("event: content_block_delta\n")
		fmt.Fprintf(&b, `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"token %d of the streamed completion "}}`, i)
		b.WriteString("\n\n")
	}
	b.WriteString("event: message_delta\n")
	b.WriteString(`data: {"type":"message_delta","delta":{},"usage":{"output_tokens":4096}}`)
	b.WriteString("\n\n")
	b.WriteString("data: [DONE]\n\n")
	return b.Bytes()
}

// parseStreamPooled mirrors the SSE parsing and buffering performed by
// streamResponse, using the pooled buffers and the compaction strategy.
// It isolates the allocation behaviour from gin and net/http.
func parseStreamPooled(stream []byte, statusCode int) (int, int) {
	src := bytes.NewReader(stream)

	bufPtr, buf := getStreamBuf()
	defer putStreamBuf(bufPtr)

	partialPtr := getPartialBuf()
	defer putPartialBuf(partialPtr)
	partial := *partialPtr

	var respBuf *bytes.Buffer
	if statusCode >= 400 {
		respBuf = getBytesBuffer()
		defer putBytesBuffer(respBuf)
	}

	var dataLines, payloadBytes int
	for {
		n, err := src.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if respBuf != nil && respBuf.Len() < maxLoggedBodyBytes {
				toWrite := len(chunk)
				if respBuf.Len()+toWrite > maxLoggedBodyBytes {
					toWrite = maxLoggedBodyBytes - respBuf.Len()
				}
				respBuf.Write(chunk[:toWrite])
			}
			partial = append(partial, chunk...)
			consumed := 0
			for {
				rest := partial[consumed:]
				idx := bytes.IndexByte(rest, '\n')
				if idx < 0 {
					break
				}
				line := bytes.TrimSpace(rest[:idx])
				consumed += idx + 1
				if !bytes.HasPrefix(line, []byte("data:")) {
					continue
				}
				payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
				if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
					continue
				}
				dataLines++
				payloadBytes += len(payload)
			}
			if consumed > 0 && consumed < len(partial) {
				copy(partial, partial[consumed:])
			}
			partial = partial[:len(partial)-consumed]
		}
		if err != nil {
			break
		}
	}
	*partialPtr = partial
	return dataLines, payloadBytes
}

// parseStreamUnpooled reproduces the pre-optimization code: a freshly
// allocated read buffer, an unconditional full-response accumulator, and a
// partial slice that is resliced forward rather than compacted.
func parseStreamUnpooled(stream []byte, statusCode int) (int, int) {
	src := bytes.NewReader(stream)

	buf := make([]byte, streamReadBufSize)
	var partial []byte
	var respBuf []byte

	var dataLines, payloadBytes int
	for {
		n, err := src.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			respBuf = append(respBuf, chunk...)
			partial = append(partial, chunk...)
			for {
				idx := bytes.IndexByte(partial, '\n')
				if idx < 0 {
					break
				}
				line := bytes.TrimSpace(partial[:idx])
				partial = partial[idx+1:]
				if !bytes.HasPrefix(line, []byte("data:")) {
					continue
				}
				payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
				if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
					continue
				}
				dataLines++
				payloadBytes += len(payload)
			}
		}
		if err != nil {
			break
		}
	}
	_ = respBuf
	return dataLines, payloadBytes
}

// TestParseStreamEquivalence pins the pooled parser to the same results as the
// original one, so the benchmark below compares two correct implementations.
func TestParseStreamEquivalence(t *testing.T) {
	for _, events := range []int{0, 1, 17, 200} {
		stream := buildSSEStream(events)
		wantLines, wantBytes := parseStreamUnpooled(stream, 200)
		gotLines, gotBytes := parseStreamPooled(stream, 200)
		if gotLines != wantLines || gotBytes != wantBytes {
			t.Errorf("events=%d: pooled parse = (%d lines, %d bytes), want (%d lines, %d bytes)",
				events, gotLines, gotBytes, wantLines, wantBytes)
		}
	}
}

// TestPooledParseReusesBuffers asserts the steady-state allocation count for a
// pooled parse is zero once the pools are warm. This is the invariant the
// //noalloc:root marker on streamResponse is meant to protect.
func TestPooledParseReusesBuffers(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping allocation test in short mode")
	}

	stream := buildSSEStream(200)
	parseStreamPooled(stream, 200) // warm the pools

	allocs := testing.AllocsPerRun(50, func() {
		parseStreamPooled(stream, 200)
	})
	// The race detector adds instrumentation that registers as allocations, so
	// under -race the check confirms "very few" rather than exactly zero.
	// raceAllocSlack is 0 in normal builds and non-zero under -race.
	if allocs > raceAllocSlack {
		t.Errorf("pooled parse allocated %.1f times per run, want ≤%.1f", allocs, raceAllocSlack)
	}
}

func BenchmarkStreamParsePooled(b *testing.B) {
	for _, events := range []int{10, 200, 2000} {
		stream := buildSSEStream(events)
		b.Run(fmt.Sprintf("events=%d", events), func(b *testing.B) {
			b.SetBytes(int64(len(stream)))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				parseStreamPooled(stream, 200)
			}
		})
	}
}

func BenchmarkStreamParseUnpooled(b *testing.B) {
	for _, events := range []int{10, 200, 2000} {
		stream := buildSSEStream(events)
		b.Run(fmt.Sprintf("events=%d", events), func(b *testing.B) {
			b.SetBytes(int64(len(stream)))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				parseStreamUnpooled(stream, 200)
			}
		})
	}
}

// BenchmarkStreamParseError covers the status>=400 path, where the body is
// accumulated for anomaly logging and capped at maxLoggedBodyBytes.
func BenchmarkStreamParseError(b *testing.B) {
	stream := buildSSEStream(200)
	b.SetBytes(int64(len(stream)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		parseStreamPooled(stream, 500)
	}
}
