# Tasks: Claude Code 流量离线滥用分析

**Input**: Design documents from `/specs/004-traffic-abuse-analysis/`

**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/internal-contracts.md, quickstart.md

**Tests**: 仅对 `internal/classify` 纯函数核心生成表驱动单测(plan 宪章自约束「可测试」明确要求)。其余层不生成测试任务。

**Organization**: 按用户故事分组。数据依赖上 US1(按人聚合报表)读取 US2(逐条打标回写)写入的标签,故 phase 顺序把 US2 管线作为真正的 MVP 切片排在前面;story 标签仍对齐 spec 编号。

## Format: `[ID] [P?] [Story] Description`

- **[P]**: 可并行(不同文件、无未完成依赖)
- **[Story]**: US1 / US2 / US3,对应 spec 用户故事
- 每个任务含明确文件路径

## Path Conventions

单体 Go 仓库:`internal/`、`cmd/`、`config/` 在仓库根。

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: 配置项与包骨架就位

- [X] T001 在 `config/config.go` 新增 `AnalyzeConfig` 结构体与 `Config.Analyze` 字段,含 `haiku_base_url` / `haiku_api_key` / `haiku_model` / `analyzer_ua` / `batch_size` / `max_retry`,嵌套 `score`(权重、baseline、threshold)、`off_hours`(start_hour/end_hour/weekend_off)、`repos`,并在 `config.Load` 里补默认值(见 data-model.md §3)
- [X] T002 [P] 在 `config/config_test.go`(如无则新建)加表驱动用例:验证 `analyze` 段的 yaml 反序列化与默认值填充

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: DB schema 与 classify 数据类型 —— 所有故事的共同前置

**⚠️ CRITICAL**: 本阶段完成前,任何用户故事都不能开工

- [X] T003 在 `internal/db/db.go` 的 `migrations` 切片追加迁移 40–44:`usage_logs` 加 `task_type TEXT NOT NULL DEFAULT ''`、`work_related INTEGER NOT NULL DEFAULT -1`、`code_direction TEXT NOT NULL DEFAULT ''`;建 `pending_analysis` 表 + 索引(见 data-model.md §1)
- [X] T004 [P] 在 `internal/model/model.go` 的 `UsageLog` 结构体补 `TaskType` / `WorkRelated` / `CodeDirection` 字段(含 `db`/`json` tag),与迁移列名对齐
- [X] T005 [P] 新建 `internal/classify/types.go`:定义 `Role` 常量、`Signal`、`Result`、`Record`、`Rollup`、`ScoreWeights`、`Config` 结构体(见 contracts §1 与 data-model §2)
- [X] T006 [P] 新建 `internal/classify/rules.go` 的 `DefaultConfig()`(后缀→方向映射、文档后缀集、内部仓库白名单),及从 `config.AnalyzeConfig` 构造 `classify.Config` 的转换函数

**Checkpoint**: schema 与类型就绪,用户故事可开工

---

## Phase 3: User Story 2 - 逐条请求被打上"在干什么"的标签 (Priority: P1) 🎯 MVP

**Goal**: 对每条**成功**的 `user_initiated` 请求,离线产出 task_type / work_related / code_direction / work_reason / doc_activity 并回写到其 `usage_logs` 行。

**Independent Test**: 灌入单条「编辑 .go 且命中内部仓库路径」的请求,跑分析,确认对应 usage_logs 行被标为 code / 后端 / work_related=1、error_reason 含 `work:命中内部仓库 X`;再灌入一条纯工具结果回灌请求,确认它被判为 tool_continuation、不入队、不消耗模型。

### Tests for User Story 2 (classify 纯函数核心)

> 先写测试并确认失败,再实现对应函数

- [X] T007 [P] [US2] 新建 `internal/classify/request_test.go`:`ParseRequest` 对字符串型/数组型 content、截断/非 JSON/无 messages 的解析与报错
- [X] T008 [P] [US2] 新建 `internal/classify/signal_test.go`:`RequestRole`(user_initiated / tool_continuation / subagent 归续跑口径)与 `Extract`(intent 截断、files basename 去重排序、repo 命中、cmds 首动词、丢弃 system/schema/历史回复)
- [X] T009 [P] [US2] 新建 `internal/classify/rules_test.go`:`Classify` 的后缀投票定 code_direction、平票留空、命中仓库定 work_related、`NeedHaiku` 触发条件

### Implementation for User Story 2

- [X] T010 [US2] 新建 `internal/classify/request.go`:`Request`/`Message`/`ContentBlock` 类型 + 自定义 `UnmarshalJSON` + `ParseRequest`(见 contracts §1.1)
- [X] T011 [US2] 新建 `internal/classify/signal.go`:`RequestRole` 与 `Extract`,含 `truncate`/`strField`/`matchRepo`/`firstVerb` 等小工具(见 contracts §1.2–1.3)
- [X] T012 [US2] 在 `internal/classify/rules.go` 实现 `Classify`(规则分类,拿不准置空 + 置 `NeedHaiku`)(见 contracts §1.4)
- [X] T013 [US2] 新建 `internal/classify/haiku.go`:`HaikuClient` + `NewHaikuClient` + `Fill`(经 ModelGate `/v1/messages`,system prompt、hint 合并、`stripFences` 解析、失败返回 error)(见 contracts §1.5)
- [X] T014 [US2] 新建 `internal/classify/analyze.go`:`Analyze` 串起 Extract→Classify→(Fill);续跑/子代理轮直接返回不调模型;`hc==nil` 为纯规则模式(见 contracts §1.6)
- [X] T015 [US2] 在 `internal/db/stats.go` 改 `BatchInsertUsageLogs`:成功行插入后取 `last_insert_rowid()`,当 `Record` 带 `Signal` 且 `StatusCode<400` 且 `Role==user_initiated` 时,于**同一事务**插入 `pending_analysis(usage_log_id, user_id, signal, ...)`(见 contracts §3、data-model 不变式 A/B)
- [X] T016 [US2] 新建 `internal/db/analysis.go`:`ListPending(limit)`(仅取未分析行)、`WriteBackResults([]Result)`(单事务内 UPDATE usage_logs + DELETE/retry pending_analysis)(见 contracts §2.1–2.2)
- [X] T017 [US2] 在 `internal/stats/collector.go` 的 `Record` 增 `Signal *classify.Signal` 与 `Role string` 字段;`recordToLog` / 批插入路径透传;避免与转发热路径耦合(信号抽取在 worker 内)
- [X] T018 [US2] 在 `internal/proxy/handler.go` 的 `emitUsage` 增加 `reqBody []byte` 入参,`statusCode<400` 时调用 `classify.ParseRequest`+`Extract`+`RequestRole` 填充 `stats.Record.Signal/Role`;两处调用点(streamResponse:869、bufferResponse:906)传入已在内存的 `reqBody`
- [X] T019 [US2] 在 `cmd/server/main.go` 注册两个 admin 端点:`GET /admin/api/analyze/pending`(调 `ListPending`)、`POST /admin/api/analyze/results`(调 `WriteBackResults`),复用现有 `X-Session-Secret` 鉴权(参照 `/admin/api/ipgeo/*` 注册方式,main.go:534/544)
- [X] T020 [US2] 在 `internal/handler/analysis.go` 实现上述两端点的 handler(请求/响应 JSON 见 contracts §2.1–2.2)
- [X] T021 [US2] 在 `cmd/check/main.go` 新增 `--analyze` flag 与 `runAnalyze(client, cfg, gatewayURL)`:循环拉批 → 对每条 `classify.Analyze`(续跑/子代理继承、纯规则或 Haiku 兜底)→ POST 回写;拉空即停

**Checkpoint**: 端到端逐条打标可用 —— 单请求可被正确分类并回写 usage_logs

---

## Phase 4: User Story 1 - 管理员识别疑似滥用的人 (Priority: P1)

**Goal**: 按人在统计窗口内聚合画像 + 滥用评分,超阈值进复核队列(附非工作原因抽样),仅识别不处罚。

**Independent Test**: 直接在 usage_logs 灌入一批已打标行(部分非工作、部分工作),调聚合,确认每人逻辑任务数/工作占比/评分正确,高分用户出现在复核队列并带原因抽样。

### Tests for User Story 1 (classify 聚合/评分)

- [X] T022 [P] [US1] 新建 `internal/classify/aggregate_test.go`:`Aggregate`(物理数 vs 逻辑数、work_related∈{0,1} 计数、off-hours、原因抽样上限)与 `Score`/`NeedsReview`(权重、baseline、threshold、LogicalTasks==0→0)

### Implementation for User Story 1

- [X] T023 [US1] 新建 `internal/classify/aggregate.go`:`Aggregate(recs, windowStart) Rollup`、`Score(r, w) float64`、`NeedsReview(r, w) bool`、`offHours(t, cfg) bool`(见 data-model §2.4–2.5)
- [X] T024 [US1] 在 `internal/db/analysis.go` 增聚合查询:按 user_id + 时间窗从 usage_logs 读回已打标行为 `[]classify.Record`(含 created_at、task_type、work_related、code_direction、tool_used 来源、error_reason 原因文本)
- [X] T025 [US1] 在 `internal/handler/insight.go` 新增 `GET /admin/api/insight/abuse`:入参时间窗,返回每人 Rollup + 评分 + 复核队列(score≥threshold)+ 原因抽样,并在 `cmd/server/main.go` 注册路由(insight 组)
- [X] T026 [US1] 在既有用量明细接口/视图中透出回写后的 task_type / code_direction / work_related / 原因文本(FR-024),定位现有 `ListUsageLogs` 响应并补字段

**Checkpoint**: 管理员可看到按人画像、评分与复核队列

---

## Phase 5: User Story 3 - 分析成本受控且不阻塞 (Priority: P2)

**Goal**: 规则优先零成本、续跑轮不调模型、Haiku 失败降级不阻塞、防分析器自递归入队。

**Independent Test**: 灌入混合请求(部分命中仓库+明确后缀,部分意图模糊),跑分析,统计模型调用次数远小于物理请求数且命中规则者零调用;模拟 Haiku 超时,确认该条保留规则结论+retry_count++,整批不中断。

### Tests for User Story 3

- [X] T027 [P] [US3] 在 `internal/classify/analyze_test.go` 加用例:命中规则→`NeedHaiku=false` 零调用;续跑/子代理→不调模型;`Fill` 返回 error 时 `Analyze` 保留规则结论(注入桩 HaikuClient)

### Implementation for User Story 3

- [X] T028 [US3] 在 `internal/proxy/handler.go` 的 `emitUsage` 入队前,按 `cfg.Analyze.AnalyzerUA` 识别分析器自身发出的 Haiku 请求并跳过入队(防自递归,data-model §2.3 第 3 条)
- [X] T029 [US3] 在 `cmd/check/main.go` 的 `runAnalyze` 里落实降级:`classify.Analyze` 返回 error(Haiku 失败/不可解析)时,该 result 置 `retry:true` 回写(`WriteBackResults` 执行 retry_count++),并对 `retry_count >= cfg.Analyze.MaxRetry` 的记录跳过并计数,批处理不中断(FR-010)
- [X] T030 [US3] 在 `runAnalyze` 结束打印本轮统计:处理数、规则命中数、Haiku 调用数、重试数、跳过数(供 SC-002/SC-003/SC-006 验证)

**Checkpoint**: 三个故事均独立可用,成本与鲁棒性达标

---

## Phase 6: Polish & Cross-Cutting Concerns

**Purpose**: 跨故事收尾与验证

- [X] T031 [P] 在 `config.yaml` 示例(或 README/部署文档)补 `analyze` 配置段样例与内部分析专用 key 说明
- [~] T032 按 `quickstart.md` 跑端到端验证:成功请求打标、失败请求不入队、续跑不计逻辑任务、回写后 pending_analysis 清空、重复运行不重复聚合(SC-005)。持久层流程已由 `internal/db/analysis_test.go` 集成测试覆盖(真实临时 SQLite:入队门槛/回写删除/幂等/重试/聚合);完整 HTTP 端到端(server + `check --analyze` 进程)留待部署环境执行
- [X] T033 [P] `go vet ./...` 与 `go test ./internal/classify/... ./config/... ./internal/db/...` 全绿,清理临时代码

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: 无依赖,立即可始
- **Foundational (Phase 2)**: 依赖 Setup;**阻塞**所有用户故事
- **US2 (Phase 3)**: 依赖 Foundational —— 产出标签的管线,MVP
- **US1 (Phase 4)**: 依赖 Foundational;可独立测试(直接灌已打标行),但**真实数据**依赖 US2 的回写
- **US3 (Phase 5)**: 对 US2 管线的成本/鲁棒性增强,依赖 US2 的 `emitUsage`/`runAnalyze` 已就位
- **Polish (Phase 6)**: 依赖需交付的故事完成

### 关键数据依赖

- US1 聚合读取 US2 回写的 `usage_logs.task_type/work_related/code_direction`。要看真实报表须先跑 US2。
- T015(入队)依赖 T005/T003(类型+schema);T018 依赖 T010–T014(classify 可用);T021 依赖 T019/T020(端点就位)。

### Within Each Story

- classify 纯函数测试先行并确认失败,再实现
- 类型/DB → classify 逻辑 → collector/proxy 采集 → admin 端点 → check CLI

### Parallel Opportunities

- T002 与 Phase 2 的 T004/T005/T006 可并行(不同文件)
- US2 三个测试文件 T007/T008/T009 可并行
- classify 实现中 request.go/signal.go/haiku.go 属不同文件,骨架就绪后可并行推进(T010/T011/T013)
- Polish 中 T031/T033 可并行

---

## Parallel Example: User Story 2 测试

```bash
# 一起启动 US2 的 classify 单测(不同文件):
Task: "internal/classify/request_test.go — ParseRequest"
Task: "internal/classify/signal_test.go — RequestRole / Extract"
Task: "internal/classify/rules_test.go — Classify / NeedHaiku"
```

---

## Implementation Strategy

### MVP First (User Story 2 管线)

1. Phase 1 Setup → 2. Phase 2 Foundational(schema+类型)→ 3. Phase 3 US2 逐条打标端到端
4. **STOP & VALIDATE**:单请求正确分类并回写 usage_logs
5. 可上线:此时明细层已能「看懂每条请求在干嘛」

### Incremental Delivery

1. Setup + Foundational → 地基就绪
2. US2 → 逐条打标(MVP,产出标签)
3. US1 → 按人聚合 + 复核队列(消费标签,交付业务价值)
4. US3 → 成本控制与降级(长期可运营)
5. Polish → 文档 + quickstart 验证

---

## Notes

- 只处理**成功**请求入队(`status_code < 400`)—— 用户约束「日志只处理成功的」,落在 T015/T018。
- 持久层任何位置都不存原始 messages,仅存 `Signal`(FR-015a / data-model 不变式 C)。
- `check --analyze` 经 admin HTTP 端点拉批/回写,不直接开库(避免与 server 争 SQLite 写锁)。
- `work_related` 用 -1 表未定;聚合只统计 ∈{0,1}。
- 未请求全量 TDD:仅 `internal/classify` 有单测任务;其余靠 quickstart 端到端验证。
