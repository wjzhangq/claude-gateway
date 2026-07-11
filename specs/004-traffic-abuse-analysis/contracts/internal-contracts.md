# 内部契约:classify 包与 check --analyze

本功能不新增对外 HTTP API(报表复用现有 `/admin/api/insight/*` 体系,评分聚合作为其数据补充)。核心契约是**内部 Go 包边界**与 **check → gateway 的增量拉取/回写 HTTP 契约**。

---

## 1. `internal/classify` 包公开契约

单包,承载请求解析、角色判定、信号抽取、规则分类、Haiku 兜底、聚合与评分。纯函数为主,便于单元测试。

### 1.1 请求解析

```go
// Request 是 Anthropic messages 请求的子集。
type Request struct {
    Model    string    `json:"model"`
    Messages []Message `json:"messages"`
}

// ParseRequest 从原始请求体解析出 Request。
// 解析失败(截断/非 JSON/无 messages)返回 error,调用方据此标记 unparseable。
func ParseRequest(body []byte) (Request, error)
```

### 1.2 角色判定

```go
type Role string

const (
    RoleUserInitiated    Role = "user_initiated"    // 计为一件独立逻辑任务
    RoleToolContinuation Role = "tool_continuation" // 工具续跑,不单独计
    RoleSubagent         Role = "subagent"          // 归为工具续跑口径(spec 决策),不单独计
)

// RequestRole 判定最后一条 user 消息是"真实指令"还是"工具结果回灌"。
func RequestRole(req Request) Role
```

### 1.3 信号抽取

```go
// Signal 是发送给 Haiku 的唯一内容(压缩后 150~400 token)。
type Signal struct {
    Intent string   `json:"intent"`          // 最后一条 user 的 text,截断 300 rune
    Files  []string `json:"files"`           // 涉及文件的 basename,去重排序
    Repo   string   `json:"repo,omitempty"`  // 命中的内部仓库名
    Cmds   []string `json:"cmds,omitempty"`  // Bash 命令的首动词,去重
    Tools  []string `json:"tools"`           // tool_use 名字集合,去重排序
}

// Extract 扫描 messages 抽出 Signal。丢弃 system prompt / 工具 schema /
// 历史 assistant 回复 / 文件正文全文。
func Extract(req Request, cfg Config) Signal
```

### 1.4 规则分类

```go
type Result struct {
    Role          Role     `json:"request_role"`
    ToolUsed      []string `json:"tool_used"`
    TaskType      string   `json:"task_type"`      // code/doc/other/""(交 Haiku)
    WorkRelated   *bool    `json:"work_related"`   // nil = 交 Haiku
    WorkReason    string   `json:"work_reason"`
    CodeDirection string   `json:"code_direction"` // 仅 code
    DocActivity   string   `json:"doc_activity"`   // 仅 doc
    NeedHaiku     bool     `json:"-"`
    FromHaiku     bool     `json:"from_haiku"`
}

// Classify 只用规则:后缀 → code_direction;命中内部仓库 → work_related=true。
// 规则拿不准的字段留空,并置 NeedHaiku。
func Classify(req Request, sig Signal, cfg Config) Result
```

### 1.5 Haiku 兜底

```go
type HaikuClient struct { /* BaseURL, APIKey, Model, HTTP */ }

func NewHaikuClient(baseURL, apiKey string) *HaikuClient

// Fill 仅当 res.NeedHaiku 时调用;把规则已定字段作为 hint 发过去,
// 模型只补空缺。失败返回 error(调用方降级:保留规则结论 + 标记待重试)。
func (c *HaikuClient) Fill(ctx context.Context, sig Signal, res *Result) error
```

**发送给 Haiku 的 body**(复用现有 ModelGate `/v1/messages`):

```json
{
  "model": "claude-haiku-4-5-20251001",
  "max_tokens": 300,
  "system": "<审计分析员 system prompt>",
  "messages": [{"role": "user", "content": "{\"signal\":{...},\"hint\":{...}}"}]
}
```

### 1.6 完整分类入口

```go
// Analyze 串起 Extract → Classify → (Fill)。tool_continuation / subagent
// 轮直接返回,不调模型。hc == nil 时纯规则模式。
func Analyze(ctx context.Context, req Request, cfg Config, hc *HaikuClient) (Result, error)
```

### 1.7 聚合与评分

```go
func Aggregate(recs []Record, windowStart time.Time) Rollup
func Score(r Rollup, w ScoreWeights) float64
func NeedsReview(r Rollup, w ScoreWeights) bool
```

契约见 [data-model.md](../data-model.md) 的 Record / Rollup / ScoreWeights。

---

## 2. check → gateway 增量分析 HTTP 契约

沿用现有模式:`check` 通过 `X-Session-Secret` 头访问 gateway `/admin/api/*`,不直接开库(避免与 server 争 SQLite 写锁)。

### 2.1 拉取待分析批次

```
GET /admin/api/analyze/pending?limit=500
Header: X-Session-Secret: <secret>
```

响应:
```json
{
  "records": [
    {
      "id": 12345,
      "usage_log_id": 999,
      "user_id": 42,
      "signal": { "intent": "...", "files": ["a.go"], "repo": "modelgate", "cmds": ["go"], "tools": ["Edit","Bash"] },
      "role": "user_initiated",
      "created_at": "2026-07-11T09:15:00Z"
    }
  ]
}
```

- 仅返回 `analyzed_at IS NULL` 的记录(增量水位线)。
- `role` 已在采集期预判;`tool_continuation`/`subagent` 记录也返回,供 check 继承父轮(check 侧不为其调用 Haiku)。

### 2.2 回写分析结论 + 请求删除中间数据

```
POST /admin/api/analyze/results
Header: X-Session-Secret: <secret>
Body:
{
  "results": [
    {
      "pending_id": 12345,
      "usage_log_id": 999,
      "task_type": "code",
      "work_related": true,
      "code_direction": "后端",
      "work_reason": "命中内部仓库 modelgate",
      "doc_activity": "",
      "from_haiku": false,
      "retry": false
    }
  ]
}
```

gateway 侧对每条 result,在**单事务**内:
1. `UPDATE usage_logs SET task_type=?, work_related=?, code_direction=?, error_reason=? WHERE id=usage_log_id`
   (`error_reason` 存放 `work_reason` 与 `doc_activity` 的合并文本)
2. `retry=false` → `DELETE FROM pending_analysis WHERE id=pending_id`
   `retry=true` → `UPDATE pending_analysis SET retry_count=retry_count+1 WHERE id=pending_id`(保留待下轮)

响应:`{"updated": 1, "deleted": 1, "retried": 0}`

### 2.3 聚合报表(复用 insight)

评分聚合结果通过 gateway 端定时任务或 `--analyze` 尾段写入(见 data-model 的 Rollup 落库策略),报表在现有 `/admin/api/insight/*` 上扩展展示,不新增独立前端契约。

---

## 3. 采集侧契约(proxy → DB)

`emitUsage` 增加 `reqBody []byte` 入参。仅当 `statusCode < 400`(**日志只处理成功的**)时:
1. `classify.Extract` 抽 Signal(在 collector 的异步 worker 内做,不占转发链路)。
2. 与 usage_logs 行在**同一事务**插入 pending_analysis,`usage_log_id = last_insert_rowid()`。

失败请求(`statusCode >= 400`)照常写 usage_logs,但**不**生成 pending_analysis 记录。
