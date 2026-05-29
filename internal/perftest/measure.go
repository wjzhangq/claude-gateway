package perftest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"time"
)

type MeasureResult struct {
	TTFT_ms            float64
	TPOT_ms            float64
	TokensPerSecond    float64
	ActualOutputTokens int
	TotalDuration_ms   float64
}

func MeasureAnthropicStream(body io.Reader) *MeasureResult {
	start := time.Now()
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 512*1024)

	var firstTokenTime time.Time
	var lastTokenTime time.Time
	tokenCount := 0
	var finalOutput int

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}

		var event struct {
			Type  string `json:"type"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
			Usage struct {
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		if event.Type == "content_block_delta" && event.Delta.Type == "text_delta" && event.Delta.Text != "" {
			now := time.Now()
			if tokenCount == 0 {
				firstTokenTime = now
			}
			lastTokenTime = now
			tokenCount++
		}

		if event.Usage.OutputTokens > 0 {
			finalOutput = event.Usage.OutputTokens
		}
	}

	total := time.Since(start)
	result := &MeasureResult{
		TotalDuration_ms: float64(total.Milliseconds()),
	}

	if tokenCount == 0 {
		return result
	}

	result.TTFT_ms = float64(firstTokenTime.Sub(start).Milliseconds())

	actualTokens := finalOutput
	if actualTokens == 0 {
		actualTokens = tokenCount
	}
	result.ActualOutputTokens = actualTokens

	if tokenCount > 1 {
		decodeTime := lastTokenTime.Sub(firstTokenTime)
		result.TPOT_ms = float64(decodeTime.Milliseconds()) / float64(tokenCount-1)
		if decodeTime.Seconds() > 0 {
			result.TokensPerSecond = float64(actualTokens) / decodeTime.Seconds()
		}
	} else {
		result.TokensPerSecond = 0
		result.TPOT_ms = 0
	}

	return result
}

func MeasureOpenAIStream(body io.Reader) *MeasureResult {
	start := time.Now()
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 512*1024)

	var firstTokenTime time.Time
	var lastTokenTime time.Time
	tokenCount := 0
	var finalOutput int

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}

		var event struct {
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		hasContent := false
		for _, ch := range event.Choices {
			if ch.Delta.Content != "" || ch.Delta.ReasoningContent != "" {
				hasContent = true
				break
			}
		}

		if hasContent {
			now := time.Now()
			if tokenCount == 0 {
				firstTokenTime = now
			}
			lastTokenTime = now
			tokenCount++
		}

		if event.Usage != nil && event.Usage.CompletionTokens > 0 {
			finalOutput = event.Usage.CompletionTokens
		}
	}

	total := time.Since(start)
	result := &MeasureResult{
		TotalDuration_ms: float64(total.Milliseconds()),
	}

	if tokenCount == 0 {
		return result
	}

	result.TTFT_ms = float64(firstTokenTime.Sub(start).Milliseconds())

	actualTokens := finalOutput
	if actualTokens == 0 {
		actualTokens = tokenCount
	}
	result.ActualOutputTokens = actualTokens

	if tokenCount > 1 {
		decodeTime := lastTokenTime.Sub(firstTokenTime)
		result.TPOT_ms = float64(decodeTime.Milliseconds()) / float64(tokenCount-1)
		if decodeTime.Seconds() > 0 {
			result.TokensPerSecond = float64(actualTokens) / decodeTime.Seconds()
		}
	} else {
		result.TokensPerSecond = 0
		result.TPOT_ms = 0
	}

	return result
}

func MeasureBedrockStream(events <-chan []byte, opts ...time.Time) *MeasureResult {
	start := time.Now()
	if len(opts) > 0 {
		start = opts[0]
	}

	var firstTokenTime time.Time
	var lastTokenTime time.Time
	tokenCount := 0
	var finalOutput int

	for eventData := range events {
		var event struct {
			Type  string `json:"type"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
			AmazonBedrockInvocationMetrics *struct {
				OutputTokenCount int `json:"outputTokenCount"`
			} `json:"amazon-bedrock-invocationMetrics"`
			Usage struct {
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}

		eventData = bytes.TrimSpace(eventData)
		if len(eventData) == 0 {
			continue
		}
		if err := json.Unmarshal(eventData, &event); err != nil {
			continue
		}

		if event.Type == "content_block_delta" && event.Delta.Type == "text_delta" && event.Delta.Text != "" {
			now := time.Now()
			if tokenCount == 0 {
				firstTokenTime = now
			}
			lastTokenTime = now
			tokenCount++
		}

		if event.Usage.OutputTokens > 0 {
			finalOutput = event.Usage.OutputTokens
		}
		if event.AmazonBedrockInvocationMetrics != nil && event.AmazonBedrockInvocationMetrics.OutputTokenCount > 0 {
			finalOutput = event.AmazonBedrockInvocationMetrics.OutputTokenCount
		}
	}

	total := time.Since(start)
	result := &MeasureResult{
		TotalDuration_ms: float64(total.Milliseconds()),
	}

	if tokenCount == 0 {
		return result
	}

	result.TTFT_ms = float64(firstTokenTime.Sub(start).Milliseconds())

	actualTokens := finalOutput
	if actualTokens == 0 {
		actualTokens = tokenCount
	}
	result.ActualOutputTokens = actualTokens

	if tokenCount > 1 {
		decodeTime := lastTokenTime.Sub(firstTokenTime)
		result.TPOT_ms = float64(decodeTime.Milliseconds()) / float64(tokenCount-1)
		if decodeTime.Seconds() > 0 {
			result.TokensPerSecond = float64(actualTokens) / decodeTime.Seconds()
		}
	} else {
		result.TokensPerSecond = 0
		result.TPOT_ms = 0
	}

	return result
}
