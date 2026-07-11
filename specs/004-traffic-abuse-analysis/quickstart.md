# Quickstart / 验证指南:Claude Code 流量离线滥用分析

本指南给出端到端验证场景,证明功能按 spec 工作。不含实现代码;实现细节见 `tasks.md` 与 `data-model.md` / `contracts/internal-contracts.md`。

## 前置条件

- Go 1.24,可 `go build ./...`
- 一个可用的 `config.yaml`(含 `server.port`、`auth.session_secret`、`database.path`)
- 配置中已填 analyze 段(见下),Haiku 走本网关自身 `/v1/messages`:

```yaml
analyze:
  enabled: true
  haiku_base_url: "http://127.0.0.1:8080"   # 本网关自身
  haiku_api_key: "<一个有效的网关 key>"
  haiku_model: "claude-haiku-4-5-20251001"
  batch_size: 500
  repos: ["teamai-iap", "modelgate", "kb-core", "thinkpet", "localclaw", "openclaw"]
  score:
    non_work: 0.6
    off_hours: 0.15
    volume: 0.25
    baseline_tasks: 60
    threshold: 0.5
  off_hours: { start: 22, end: 8, weekend: true }
```

## 构建

```bash
go build ./...              # 全部包编译通过
go test ./internal/classify/...   # 分类/聚合/评分单测
go vet ./...
```

## 场景 1:成功请求入待分析队列,失败请求不入(FR-001/002,用户约束)

1. 启动 server:`./bin/server --config config.yaml`
2. 用有效 key 发一条会 200 的 `/v1/messages` 请求(带真实 user text)。
3. 再发一条会 4xx/5xx 的请求(如无效 model)。
4. **预期**:
   - `usage_logs` 出现两行(成功与失败都记账)。
   - `pending_analysis` 只出现**成功**那条对应的行(`status_code < 400`),且 `usage_log_id` 指向它、`signal` 非空、不含原始 messages 全文。
   - `check --analyze` 待处理计数 = 1。

```bash
sqlite3 data/gateway.db "SELECT count(*) FROM pending_analysis;"          # 期望 1
sqlite3 data/gateway.db "SELECT length(signal) FROM pending_analysis;" # 数百字节量级,非整包
```

## 场景 2:规则命中,零模型调用(FR-007,SC-002)

1. 发一条编辑 `.go` 文件、路径含内部仓库名(如 `teamai-iap/auth.go`)的成功请求。
2. 运行:`./bin/check --config config.yaml --analyze`
3. **预期**:
   - 该请求完全由规则定性:`task_type=code`、`code_direction=后端`、`work_related=1`、`error_reason` 前缀含 `命中内部仓库 teamai-iap`。
   - 分析日志显示本批 Haiku 调用次数为 **0**(该条 `from_haiku=false`)。
   - 对应 `pending_analysis` 行被删除。

```bash
./bin/check --config config.yaml --analyze
sqlite3 data/gateway.db "SELECT task_type, code_direction, work_related, error_reason FROM usage_logs ORDER BY id DESC LIMIT 1;"
sqlite3 data/gateway.db "SELECT count(*) FROM pending_analysis;"   # 期望回落
```

## 场景 3:规则拿不准 → Haiku 兜底(FR-008,US2 场景 3)

1. 发一条**无文件线索、意图模糊**的成功请求(纯自然语言问答,无 tool_use)。
2. 运行 `--analyze`。
3. **预期**:该条触发一次 Haiku 调用,回写 `task_type`(可能 other)、`work_related`,`from_haiku=true`。发给 Haiku 的 body 只含压缩信号 + hint,不含原始 messages(可在 debug 日志核对请求体大小)。

## 场景 4:工具续跑/子代理不计逻辑任务、不调模型(FR-006/009)

1. 构造一条 messages 末尾只有 `tool_result`(无新 user text)的成功请求。
2. **预期**:
   - 该请求在采集侧被判为 `tool_continuation`,**不写** `pending_analysis`(不单独分析),或写入但标记角色后聚合时不计逻辑任务(以 data-model 的采集侧过滤为准:仅 `user_initiated` 入队)。
   - `--analyze` 不为其产生模型调用。

## 场景 5:Haiku 失败降级不中断整批(FR-010,SC-006)

1. 临时把 `analyze.haiku_api_key` 改成无效值,制造调用失败。
2. 灌入若干条「需兜底」的请求 + 若干「规则可定」的请求,运行 `--analyze`。
3. **预期**:
   - 规则可定的照常回写并删除队列行。
   - 需兜底的那些:保留规则已定结论,`pending_analysis.retry_count` +1、`last_error` 记录原因,**不删除**、**不中断**整批。
   - 进程退出码为 0,日志汇总「成功 N、待重试 M」。

## 场景 6:回写后即删,可重复运行不重复聚合(FR-014/015,SC-005)

1. 连续运行两次 `--analyze`。
2. **预期**:第二次「本批处理 0 条」(已分析的不再出现);`usage_logs` 不出现重复回写;聚合结果稳定。

## 场景 7:按人聚合 + 复核队列(US1,FR-019~021,SC-007)

1. 造两个用户:A 全部命中内部仓库(工作相关),B 多为非工作(如个人副业脚本、无内部仓库、意图明显私人)。
2. 运行 `--analyze` 完成打标。
3. 拉取报表:

```bash
curl -s -H "X-Session-Secret: $SECRET" \
  "http://127.0.0.1:8080/admin/api/insight/abuse?window=day" | jq .
```

4. **预期**:
   - 每个活跃用户返回 `physical_count / logical_tasks / task_type_count / code_dir_count / work_tasks / non_work_tasks / off_hours_tasks / abuse_score`。
   - B 的 `abuse_score >= threshold`,进入 `review_queue`,并带 `non_work_examples`(work_reason 抽样)。
   - A 的 `abuse_score` 低、不在队列。
   - 响应不含任何自动处罚字段/动作(只识别不处罚)。

## 场景 8:热路径零延迟回归(SC-001)

- 对比开启/关闭 `analyze.enabled` 两种配置下,转发链路 P99 延迟无可测量差异(信号抽取在响应回写之后、代理协程内处理内存已有 `reqBody`,且写队列走既有异步 collector 通道)。
- 可用现有 `perftest` 通道或外部压测工具各跑一轮比对。

## 通过标准回溯

| 场景 | 覆盖的 SC/FR |
|------|-------------|
| 1 | FR-001/002/003,用户约束「只处理成功」 |
| 2 | FR-007,SC-002 |
| 3 | FR-008/011,US2-3 |
| 4 | FR-006/009 |
| 5 | FR-010,SC-006 |
| 6 | FR-012/014/015/015a,SC-005 |
| 7 | FR-016~024,SC-007 |
| 8 | SC-001 |
