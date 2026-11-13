package perftest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/wjzhangq/claude-gateway/config"
)

type ChannelConfig struct {
	Name        string `json:"name"`
	Model       string `json:"model"`
	BackendName string `json:"backend_name,omitempty"`
}

type CellResult struct {
	Channel            string  `json:"channel"`
	Model              string  `json:"model"`
	InputTokens        int     `json:"input_tokens"`
	MaxTokens          int     `json:"max_tokens"`
	TTFT_ms            float64 `json:"ttft_ms"`
	TPOT_ms            float64 `json:"tpot_ms"`
	TokensPerSecond    float64 `json:"tokens_per_second"`
	ActualOutputTokens int     `json:"actual_output_tokens"`
	TotalDuration_ms   float64 `json:"total_duration_ms"`
	Status             string  `json:"status"`
	ErrorMsg           string  `json:"error_msg,omitempty"`
}

type BackendPicker interface {
	PickForPerfTest() (name, url, apiKey string, client *http.Client, ok bool)
	PickByNameForPerfTest(name string) (url, apiKey string, client *http.Client, ok bool)
}

// BedrockInvoker is retained as an empty placeholder so NewRunner keeps its
// arity for existing callers. The AWS channel is retired and callers pass nil.
type BedrockInvoker interface{}

type Runner struct {
	backendPicker BackendPicker
	config        *config.Config
	mu            sync.Mutex
	running       bool
}

func NewRunner(bp BackendPicker, _ BedrockInvoker, cfg *config.Config) *Runner {
	return &Runner{
		backendPicker: bp,
		config:        cfg,
	}
}

func (r *Runner) IsRunning() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running
}

func (r *Runner) SetRunning(v bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.running = v
}

func (r *Runner) RunCell(ctx context.Context, ch ChannelConfig, inputTokens, maxTokens int) *CellResult {
	result := &CellResult{
		Channel:     ch.Name,
		Model:       ch.Model,
		InputTokens: inputTokens,
		MaxTokens:   maxTokens,
	}

	cellCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	switch ch.Name {
	case "backend":
		r.runBackendCell(cellCtx, ch, inputTokens, maxTokens, result)
	case "aws":
		result.Status = "error"
		result.ErrorMsg = "the aws channel has been retired"
	default:
		r.runPublicCell(cellCtx, ch.Name, ch.Model, inputTokens, maxTokens, result)
	}

	return result
}

func (r *Runner) runBackendCell(ctx context.Context, ch ChannelConfig, inputTokens, maxTokens int, result *CellResult) {
	var url, apiKey string
	var client *http.Client
	var ok bool

	if ch.BackendName != "" {
		url, apiKey, client, ok = r.backendPicker.PickByNameForPerfTest(ch.BackendName)
		if !ok {
			result.Status = "error"
			result.ErrorMsg = fmt.Sprintf("backend %q not found or disabled", ch.BackendName)
			return
		}
	} else {
		_, url, apiKey, client, ok = r.backendPicker.PickForPerfTest()
		if !ok {
			result.Status = "error"
			result.ErrorMsg = "no healthy backend available"
			return
		}
	}

	body := BuildAnthropicBody(inputTokens, maxTokens, ch.Model)
	bodyBytes, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(url, "/")+"/v1/messages", bytes.NewReader(bodyBytes))
	if err != nil {
		result.Status = "error"
		result.ErrorMsg = fmt.Sprintf("build request: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := client.Do(req)
	if err != nil {
		result.Status = "error"
		result.ErrorMsg = fmt.Sprintf("request failed: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		result.Status = "error"
		result.ErrorMsg = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 200))
		return
	}

	m := MeasureAnthropicStream(resp.Body)
	result.TTFT_ms = m.TTFT_ms
	result.TPOT_ms = m.TPOT_ms
	result.TokensPerSecond = m.TokensPerSecond
	result.ActualOutputTokens = m.ActualOutputTokens
	result.TotalDuration_ms = m.TotalDuration_ms
	result.Status = "success"
}

func (r *Runner) runPublicCell(ctx context.Context, _, model string, inputTokens, maxTokens int, result *CellResult) {
	provider := r.config.LookupPublicProvider(model, "")
	if provider == nil {
		result.Status = "error"
		result.ErrorMsg = fmt.Sprintf("no public provider found for model %s", model)
		return
	}

	targetURL := provider.OpenAIURL
	if targetURL == "" {
		result.Status = "error"
		result.ErrorMsg = "public provider has no openai_url configured"
		return
	}

	body := BuildOpenAIBody(inputTokens, maxTokens, model)
	bodyBytes, _ := json.Marshal(body)

	base := strings.TrimRight(targetURL, "/")
	path := "/v1/chat/completions"
	if strings.HasSuffix(base, "/v1") {
		path = "/chat/completions"
	}
	req, err := http.NewRequestWithContext(ctx, "POST", base+path, bytes.NewReader(bodyBytes))
	if err != nil {
		result.Status = "error"
		result.ErrorMsg = fmt.Sprintf("build request: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+provider.APIKey)

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		result.Status = "error"
		result.ErrorMsg = fmt.Sprintf("request failed: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		result.Status = "error"
		result.ErrorMsg = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 200))
		return
	}

	m := MeasureOpenAIStream(resp.Body)
	result.TTFT_ms = m.TTFT_ms
	result.TPOT_ms = m.TPOT_ms
	result.TokensPerSecond = m.TokensPerSecond
	result.ActualOutputTokens = m.ActualOutputTokens
	result.TotalDuration_ms = m.TotalDuration_ms
	result.Status = "success"
}

func matchGlob(pattern, name string) (bool, error) {
	hasWild := false
	for i := 0; i < len(pattern); i++ {
		if pattern[i] == '*' {
			hasWild = true
			break
		}
	}
	if !hasWild {
		return pattern == name, nil
	}
	if pattern[len(pattern)-1] == '*' {
		prefix := pattern[:len(pattern)-1]
		return len(name) >= len(prefix) && name[:len(prefix)] == prefix, nil
	}
	return false, nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
