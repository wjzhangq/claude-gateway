# Phase 1 Data Model: Claude Code 流量离线滥用分析

## 1. 数据库 schema 变更(迁移 40+)

追加到 `internal/db/db.go` 的 `migrations` 切片(当前末位 39,故从 40 起)。

```sql
-- 40: usage_logs 承载分析结论(3 列)
ALTER TABLE usage_logs ADD COLUMN task_type      TEXT    NOT NULL DEFAULT '';  -- code|doc|other|''(未分析)
-- 41
ALTER TABLE usage_logs ADD COLUMN work_related   INTEGER NOT NULL DEFAULT -1;  -- 1=是 0=否 -1=未定
-- 42
ALTER TABLE usage_logs ADD COLUMN code_direction TEXT    NOT NULL DEFAULT '';  -- 前端/后端/... 仅 code

-- 43: 待分析队列(= 增量水位线;只存尚未分析成功的记录)
CREATE TABLE IF NOT EXISTS pending_analysis (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    usage_log_id  INTEGER NOT NULL,              -- 回写目标 usage_logs.id
    user_id       INTEGER NOT NULL,              -- 冗余,便于聚合/排查
    signal        TEXT    NOT NULL,              -- classify.Signal 的 JSON(压缩信号,无原始 messages)
    retry_count   INTEGER NOT NULL DEFAULT 0,    -- Haiku 兜底失败重试计数
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
-- 44
CREATE INDEX IF NOT EXISTS idx_pending_analysis_id ON pending_analysis(id);
```

> `work_related` 用 `-1` 表示「未分析/未定」,避免与 0(否)混淆。回写时置 0/1。
> `error_reason`(迁移 36 已存在)复用:成功请求上原为空,分析回写 `work_reason` / `doc_activity`,格式 `work:<reason>;doc:<activity>`(任一为空则省略该段)。

## 2. 实体

### 2.1 `classify.Signal`(压缩信号,持久化为 pending_analysis.signal)

| 字段 | 类型 | 说明 |
|------|------|------|
| Intent | string | 最后一条 user 的 text,截断 ≤300 字 |
| Files | []string | tool_use 里的文件 basename 去重排序 |
| Repo | string | 命中的内部仓库名(空=未命中) |
| Cmds | []string | Bash 命令首动词去重 |
| Tools | []string | 用到的工具名去重排序 |

约束:序列化后典型 150~400 token;**绝不含** system prompt / 工具 schema / 历史 assistant 回复 / 文件正文(FR-004、FR-011)。

### 2.2 `classify.Result`(分类结论)

| 字段 | 类型 | 来源 | 落点 |
|------|------|------|------|
| Role | string | 规则 | 决定是否入队(不落 usage_logs) |
| ToolUsed | []string | 规则 | (聚合用,来自 signal.Tools) |
| TaskType | string | 规则→Haiku | usage_logs.task_type |
| WorkRelated | *bool | 规则→Haiku | usage_logs.work_related(0/1) |
| WorkReason | string | 规则/Haiku | usage_logs.error_reason(work: 段) |
| CodeDirection | string | 规则→Haiku | usage_logs.code_direction |
| DocActivity | string | Haiku | usage_logs.error_reason(doc: 段) |
| NeedHaiku | bool | 规则 | 内部,不持久化 |
| FromHaiku | bool | 运行时 | 可选,不持久化(可进日志) |

取值域:`TaskType ∈ {code, doc, other, ""}`;`CodeDirection ∈ {前端,后端,固件,运维,移动端,数据,脚本,测试,其他,""}`(仅 code)。

### 2.3 `pending_analysis` 行

写入条件(**全部满足**才入队,见 research R3–R6):
- `StatusCode < 400`(只处理成功请求 — 用户约束)
- `RequestRole == "user_initiated"`(工具续跑/子代理不入队)
- 请求**不是**分析器自身发出的 Haiku 调用(按 UA/专用 key 识别,防自递归)

生命周期:入队 → `check --analyze` 取出分析 → 回写 usage_logs → **删除本行**;失败则 `retry_count++` 留待重试,超上限跳过。

### 2.4 `classify.Rollup`(按人聚合,读时计算,不落表)

| 字段 | 说明 |
|------|------|
| Identity(UserID) | 聚合主键 |
| WindowStart | 统计窗口起点 |
| PhysicalCount | 物理请求数(该窗口 usage_logs 行数) |
| LogicalTasks | 逻辑任务数(task_type 非空的成功 user_initiated 行数) |
| TaskTypeCount / CodeDirCount / ToolCount | 分布 |
| WorkTasks / NonWorkTasks | work_related=1 / =0 计数 |
| OffHoursTasks | created_at 落在非工作时段的逻辑任务数 |
| NonWorkExample | error_reason 里 work_reason 抽样(≤10) |
| AbuseScore | 0~1,见评分 |

### 2.5 滥用评分(`classify.ScoreWeights`,可配置)

```
nonWork  = NonWorkTasks / LogicalTasks
offHours = OffHoursTasks / LogicalTasks
vol      = clamp((LogicalTasks - BaselineTasks) / BaselineTasks, 0, 1)   // 仅超基线才计
score    = clamp(NonWork*nonWork + OffHours*offHours + Volume*vol, 0, 1)
```

默认权重:`NonWork=0.6, OffHours=0.15, Volume=0.25, BaselineTasks=60, Threshold=0.5`(FR-020/FR-022)。`LogicalTasks==0` → score=0。`score >= Threshold` → 进复核队列(FR-021),**不做任何自动处罚**。

## 3. 配置新增(`config.Config`,yaml)

```yaml
analyze:
  haiku_base_url: "http://127.0.0.1:8080"   # 默认指向本网关自身
  haiku_api_key:  "<内部分析专用 key>"
  haiku_model:    "claude-haiku-4-5-20251001"
  analyzer_ua:    "claude-gateway-analyzer"  # 代理侧据此跳过自递归入队
  batch_size:     500
  max_retry:      3
  score:
    non_work: 0.6
    off_hours: 0.15
    volume: 0.25
    baseline_tasks: 60
    threshold: 0.5
  off_hours:
    start_hour: 22      # 含
    end_hour: 8         # 不含
    weekend_off: true
  repos: ["teamai-iap","modelgate","kb-core","thinkpet","localclaw","openclaw"]
```

## 4. 关系与不变式

- `pending_analysis.usage_log_id` → `usage_logs.id`(逻辑外键,同事务写入保证不产生孤儿)。
- 不变式 A:`pending_analysis` 中每行对应的 `usage_logs.task_type` 在回写前为 `''`;回写成功后该行被删除 ⇒ 队列里不存在「已分析」记录(SC-005 幂等)。
- 不变式 B:失败请求(status≥400)永不出现在 `pending_analysis`。
- 不变式 C:持久层任何位置都不含原始 messages,仅含 `Signal`(FR-015a)。
- 不变式 D:`work_related ∈ {-1,0,1}`;聚合只统计 `∈{0,1}` 的逻辑任务。
