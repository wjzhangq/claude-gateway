# Implementation Plan: Claude Code 流量离线滥用分析

**Branch**: `004-traffic-abuse-analysis` | **Date**: 2026-07-11 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `/specs/004-traffic-abuse-analysis/spec.md`

## Summary

在现有网关(ModelGate 旁路)上,为每条**成功**转发的 Claude Code 请求离线打标(工具 / 任务类型 / 代码方向或文档动作 / 是否工作相关),并按人聚合出滥用评分、生成人工复核队列。

技术路径:请求转发完成后(响应已回给客户端,不在关键路径)在代理侧从内存中的原始 `reqBody` 抽取**压缩信号** + 判定**请求角色**,仅对 `status_code < 400` 且为 `user_initiated` 的请求把信号写入 `pending_analysis` 表(与 `usage_logs` 行原子关联)。原始正文不落库。离线 `check --analyze` 消费待分析队列:规则优先分类,规则拿不准才调用 Haiku(经本网关自身 `/v1/messages` 转发),结论回写 `usage_logs`(新增 3 列 + 复用 `error_reason`),回写成功即删除对应 `pending_analysis` 行。按人聚合与评分直接从回写后的 `usage_logs` 计算,经新增 admin 端点暴露。

## Technical Context

**Language/Version**: Go 1.24

**Primary Dependencies**: gin(HTTP)、`modernc.org/sqlite`(纯 Go SQLite,WAL)、logrus、yaml.v3;复用内部包 `internal/tokenest`(`ExtractRequestText`)、`internal/db`、`internal/stats`、`internal/auth`

**Storage**: SQLite(单文件,WAL,`busy_timeout=5000`,`_txlock=immediate`)。服务端为唯一写者;`check --analyze` **不直接开库**,而是经 gateway 的 `/admin/api/analyze/*` 端点拉取待分析批次、回写结论(沿用既有 `--ip2region` 的 `X-Session-Secret` HTTP 模式),避免两个进程争 SQLite 写锁

**Testing**: `go test`(表驱动单测,已有 `internal/proxy/*_test.go`、`internal/ipgeo/*_test.go`、`config/*_test.go` 先例)

**Target Platform**: Linux 服务器(旁路批处理进程)

**Project Type**: 单体后端服务 + 配套 CLI(`cmd/server`、`cmd/check`)

**Performance Goals**: 分析为旁路;转发链路 P99 延迟无可测量增加(信号抽取在响应回写之后、异步 collector 之前的代理协程内完成,仅处理内存中已有的 `reqBody`)

**Constraints**:
- 只分析**成功**请求(`status_code < 400`);失败请求照常记 `usage_logs` 但不入 `pending_analysis`(用户约束「日志只处理成功的」)
- 原始 messages 不落库;仅持久化压缩信号,回写后即删信号(FR-015a)
- 发给 Haiku 的仅为压缩信号 + hint(几百 token),绝不含原始 messages(FR-011)
- 单条 Haiku 失败不得中断整批(FR-010)

**Scale/Scope**: 日级数万请求;`check --analyze` 分批(默认 500/批)推进,可重复运行只处理新增

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

`.specify/memory/constitution.md` 仍为**未填充模板**(全部占位符),无可执行的项目宪章条款。因此无硬性门禁可校验。

采用的通用工程约束(与现有代码库既有实践一致,作为自我约束):
- **不阻塞热路径**:分析旁路化,信号抽取只在响应完成后处理内存已有数据 —— PASS
- **复用既有结构**:走既有 collector/DB/迁移/CLI 模式,不引入新框架 —— PASS
- **可测试**:纯函数分类逻辑集中在 `internal/classify`,表驱动单测 —— PASS
- **最小新增列**:`usage_logs` 只加 3 列(`task_type`/`work_related`/`code_direction`),文本复用 `error_reason` —— PASS

> 若后续补齐 constitution 并引入「测试优先」等强条款,需在实现前回到本节重估。

## Project Structure

### Documentation (this feature)

```text
specs/004-traffic-abuse-analysis/
├── plan.md              # 本文件
├── research.md          # Phase 0 输出
├── data-model.md        # Phase 1 输出
├── quickstart.md        # Phase 1 输出
├── contracts/           # Phase 1 输出
│   └── internal-contracts.md   # classify 包接口 + check --analyze CLI + admin 端点契约
└── tasks.md             # /speckit-tasks 产出(本命令不创建)
```

### Source Code (repository root)

```text
internal/
├── classify/                 # 新增:单包分类逻辑(纯函数为主)
│   ├── request.go            # Anthropic messages 子集解析(Message/ContentBlock/Request)
│   ├── signal.go             # Extract() 压缩信号 + RequestRole()
│   ├── rules.go              # Config/DefaultConfig、Classify() 规则分类
│   ├── haiku.go              # HaikuClient.Fill() 兜底(经本网关 /v1/messages)
│   ├── aggregate.go          # Rollup 聚合 + score() 滥用评分(可配置权重)
│   └── *_test.go             # 表驱动单测
├── proxy/
│   └── handler.go            # 改:emitUsage 接收 reqBody,抽信号+判角色,附到 stats.Record
├── stats/
│   └── collector.go          # 改:Record 增 Signal/RequestRole 字段;成功且 user_initiated 时同事务写 pending_analysis
├── db/
│   ├── db.go                 # 改:迁移 40+(usage_logs 加列、建 pending_analysis 表)
│   ├── stats.go              # 改:批插入返回/回填 usage_log id;新增 pending_analysis CRUD
│   └── analysis.go           # 新增:ListPending / WriteBackResult(事务内更新+删/重试) / 聚合查询
└── handler/
    ├── insight.go            # 改:新增 GET /admin/api/insight/abuse(按人聚合+复核队列)
    └── analysis.go           # 新增:GET /admin/api/analyze/pending、POST /admin/api/analyze/results

cmd/
└── check/
    └── main.go               # 改:新增 --analyze 子命令(经 admin HTTP 端点拉批/回写,调 classify;不直接开库)

config/
└── config.go                 # 改:新增 analyze 配置段(Haiku base/key/model、评分权重、时间窗、阈值)
```

**Structure Decision**: 沿用单体仓库既有分层。分类核心逻辑独立成 `internal/classify` 纯逻辑包(便于单测、被代理侧与 CLI 侧共用):代理侧用它做**零成本**的信号抽取与角色判定;`check --analyze` 用它做规则分类 + Haiku 兜底 + 聚合评分。持久化与迁移沿用 `internal/db`;CLI 沿用 `cmd/check` 既有子命令风格(flag + `X-Session-Secret` 直连 gateway HTTP 端点,如同 `--ip2region`/`--health`),而非直接开库,避免与服务端争 SQLite 写锁。

## Complexity Tracking

> 无 constitution 门禁违规需要豁免。本节留空。
