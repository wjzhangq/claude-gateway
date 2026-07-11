package classify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// haikuSystemPrompt frames Haiku as an audit analyst that must return ONLY a JSON
// object. It classifies from the compressed signal alone — it never sees raw
// messages — and is told to fill only the fields the rules left blank.
const haikuSystemPrompt = `你是一名研发流量审计分析员。你只会收到一段"压缩信号"(JSON)和已由规则判定的部分结论(hint)。` +
	`请仅依据信号补全缺失字段,不要推翻 hint 中已给出的非空值。` +
	`只输出一个 JSON 对象,不要任何解释或代码块围栏,字段如下:` +
	`{"task_type":"code|doc|other","work_related":true|false,"code_direction":"前端|后端|固件|运维|移动端|数据|脚本|测试|其他|",` +
	`"doc_activity":"简述文档动作或空","work_reason":"简述是否工作相关的原因"}。` +
	`task_type=code 时给出 code_direction;task_type=doc 时给出 doc_activity;无法判断的字段留空字符串。`

// HaikuClient calls a Haiku model through the gateway's own /v1/messages endpoint
// (BaseURL). Reusing the gateway means the analyzer's own calls are load-balanced,
// billed, and logged like any other request.
type HaikuClient struct {
	BaseURL string
	APIKey  string
	Model   string
	UA      string // sent as User-Agent so the gateway can skip enqueue (anti-recursion)
	HTTP    *http.Client
}

// NewHaikuClient builds a client with a sane timeout. Model defaults to
// claude-haiku-4-5 when empty; callers normally pass cfg.Analyze.HaikuModel.
func NewHaikuClient(baseURL, apiKey, model string) *HaikuClient {
	if model == "" {
		model = "claude-haiku-4-5-20251001"
	}
	return &HaikuClient{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		Model:   model,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

// haikuReq is the Anthropic /v1/messages body we send. max_tokens is kept small
// because we only expect a compact JSON verdict.
type haikuReq struct {
	Model     string        `json:"model"`
	MaxTokens int           `json:"max_tokens"`
	System    string        `json:"system"`
	Messages  []haikuReqMsg `json:"messages"`
}

type haikuReqMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// haikuResp is the subset of the response we read: the text content blocks plus
// the usage block so the analyzer can account for the tokens its Haiku fallback
// spends (these calls bypass usage_logs when routed straight to a backend node).
type haikuResp struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// haikuVerdict is the JSON we ask Haiku to return.
type haikuVerdict struct {
	TaskType      string `json:"task_type"`
	WorkRelated   *bool  `json:"work_related"`
	CodeDirection string `json:"code_direction"`
	DocActivity   string `json:"doc_activity"`
	WorkReason    string `json:"work_reason"`
}

// Fill sends the signal + the rules' partial verdict (as a hint) to Haiku and
// merges back ONLY the fields the rules left empty. It mutates res in place and
// sets res.FromHaiku on success. On any failure (transport, non-200, unparseable
// output) it returns an error and leaves res untouched, so the caller can keep the
// rule-only verdict and mark the record for retry (FR-010).
func (c *HaikuClient) Fill(ctx context.Context, sig Signal, res *Result) error {
	// The user message carries the signal and the already-decided hint fields.
	hint := map[string]any{
		"task_type":      res.TaskType,
		"code_direction": res.CodeDirection,
	}
	if res.WorkRelated != nil {
		hint["work_related"] = *res.WorkRelated
		hint["work_reason"] = res.WorkReason
	}
	payload := map[string]any{"signal": sig, "hint": hint}
	userContent, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal haiku payload: %w", err)
	}

	body, err := json.Marshal(haikuReq{
		Model:     c.Model,
		MaxTokens: 300,
		System:    haikuSystemPrompt,
		Messages:  []haikuReqMsg{{Role: "user", Content: string(userContent)}},
	})
	if err != nil {
		return fmt.Errorf("marshal haiku request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	// Send both auth headers: the gateway reads Authorization: Bearer, while a raw
	// backend node (used when haiku_base_url is empty) expects Anthropic's x-api-key.
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("x-api-key", c.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	if c.UA != "" {
		req.Header.Set("User-Agent", c.UA)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("haiku request: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("haiku returned HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}

	var hr haikuResp
	if err := json.Unmarshal(raw, &hr); err != nil {
		return fmt.Errorf("decode haiku response: %w", err)
	}
	var text strings.Builder
	for _, b := range hr.Content {
		if b.Type == "text" {
			text.WriteString(b.Text)
		}
	}
	var v haikuVerdict
	if err := json.Unmarshal([]byte(stripFences(text.String())), &v); err != nil {
		return fmt.Errorf("parse haiku verdict: %w", err)
	}

	mergeVerdict(res, v)
	res.FromHaiku = true
	res.HaikuInTok = hr.Usage.InputTokens
	res.HaikuOutTok = hr.Usage.OutputTokens
	return nil
}

// mergeVerdict fills only the fields the rules left empty, so Haiku never
// overrides a confident rule decision.
func mergeVerdict(res *Result, v haikuVerdict) {
	if res.TaskType == "" && v.TaskType != "" {
		res.TaskType = v.TaskType
	}
	if res.WorkRelated == nil && v.WorkRelated != nil {
		res.WorkRelated = v.WorkRelated
		if res.WorkReason == "" {
			res.WorkReason = v.WorkReason
		}
	}
	if res.TaskType == "code" && res.CodeDirection == "" && v.CodeDirection != "" {
		res.CodeDirection = v.CodeDirection
	}
	if res.TaskType == "doc" && res.DocActivity == "" && v.DocActivity != "" {
		res.DocActivity = v.DocActivity
	}
}

// stripFences removes a leading ```json / ``` fence and a trailing ``` so a
// fenced reply still parses. It also trims surrounding whitespace.
func stripFences(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// Drop the first line (``` or ```json) and any trailing fence.
	if nl := strings.IndexByte(s, '\n'); nl >= 0 {
		s = s[nl+1:]
	}
	s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	return strings.TrimSpace(s)
}
