# Phase 0 Research: Claude Code 流量离线滥用分析

本文件解决 plan 中的技术未知点。每项:决策 / 理由 / 备选与否决原因。

## R1. 信号抽取在哪一层做?

**决策**:在 `internal/proxy/handler.go` 的响应完成路径(`streamResponse`/`bufferResponse` → `emitUsage`)内做,把内存中已有的 `reqBody` 传入 `emitUsage`,调用 `classify.Extract` + `classify.RequestRole`,把结果塞进 `stats.Record`。

**理由**:
- `reqBody` 在这两个函数里本就存在(用于 token 估算与 `logBackendAnomaly`),无额外读取成本。
- 此路径在响应已回写客户端**之后**执行,不在转发关键路径,满足 SC-001(P99 无可测量增加)。
- 抽取是纯 CPU 的字符串扫描(几百 µs 量级),且随后 collector 本就是异步批写。

**备选与否决**:
- *在中间件里抽取*:中间件在请求进入时执行、`resp` 未知,拿不到 `status_code`,无法只处理成功请求。否决。
- *离线从原始日志重放抽取*:需要把原始 messages 落库或落文件长期保留,违反 FR-015a(不长期留存原始正文)。否决。

## R2. 原始正文如何不落库、又满足「只持久化压缩信号」?

**决策**:原始 `reqBody` 只存在于代理协程的内存中,用完即随请求结束被 GC。持久化的只有 `classify.Signal`(intent≤300 字 + 文件名/仓库/命令动词/工具名),以 JSON 存入 `pending_analysis.signal`。

**理由**:现状代理**从不**把请求正文写进 SQLite(只有 `logBackendAnomaly` 会把异常请求写进文件日志)。因此「分析后删除原始正文」(FR-015a)对 DB 而言天然成立 —— DB 里从一开始就没有原始正文,只有压缩信号,信号又在回写后删除。

**备选与否决**:
- *先落原始正文、分析后删*:引入大字段写放大 + 删除竞态 + 隐私面扩大,且用户已确认「分析后即删全部原始内容」。否决。

## R3. pending_analysis 行如何与 usage_logs 行关联(拿到自增 id)?

**决策**:改造持久化路径,使**成功且 user_initiated** 的记录在**同一事务**内先 `INSERT usage_logs` 再用 `last_insert_rowid()` 拿到 id,紧接着 `INSERT pending_analysis(usage_log_id, user_id, signal, created_at)`。

**理由**:
- `usage_logs.id` 是回写目标主键;`pending_analysis` 必须携带它,否则分析完无处回写。
- 同事务保证「有 pending 必有对应 usage_log」,消除孤儿行。
- 现有 `BatchInsertUsageLogs` 用 prepared stmt 批插入、不取 id;需要一条**带信号的记录走单行插入 + 取 id** 的新路径,普通记录仍走批量。

**实现要点**:
- `stats.Record` 增 `Signal *classify.Signal` 与 `RequestRole string`。
- collector worker 分流:`Signal != nil && StatusCode < 400 && RequestRole=="user_initiated"` 的记录走 `InsertUsageLogReturningID` + `InsertPending`(可仍批量化为「逐行插入取 id」的小事务);其余走既有 `BatchInsertUsageLogs`。
- 失败请求(`status_code >= 400`):照常进 `usage_logs`,**不**进 `pending_analysis`(用户约束)。

**备选与否决**:
- *全部改成逐行插入取 id*:牺牲现有批量写吞吐,且绝大多数记录(工具续跑轮、失败请求)不需要 id。否决,仅对需分析的子集逐行取 id。
- *按 (user_id, created_at, api_key_id) 事后 JOIN 匹配*:无唯一键、并发下易错配。否决。

## R4. 只分析成功请求(用户约束「日志只处理成功的」)如何落地?

**决策**:在写 `pending_analysis` 的判定里加 `StatusCode < 400` 门槛。`usage_logs` 仍记录全部请求(成功+失败),分析队列只纳入成功请求。

**理由**:
- 失败请求(限流、鉴权失败、上游 5xx)往往不含完整用户意图或根本没转发成功,分析价值低且浪费 Haiku 成本。
- 与既有约定一致:`emitUsage` 里 `AddDailyCost` 也只在 `statusCode < 400` 累加。

**备选与否决**:*分析所有请求再在聚合层过滤*:白白存储与分析失败请求信号,违背省成本目标。否决。

## R5. 请求角色判定与「逻辑任务数」口径

**决策**:`RequestRole()` 返回 `user_initiated` / `tool_continuation`;`subagent` 归入 `tool_continuation`(spec 已确认子代理不单独计)。仅 `user_initiated` 且成功的请求进 `pending_analysis` 并计为一件逻辑任务。工具续跑/子代理轮不入队、不调模型。

**理由**:直接实现 FR-006/FR-009 与 SC-003(调用次数按逻辑任务数计,较物理请求数降≥5×)。把「续跑不入队」前移到代理侧,连 pending 行都不产生,比「入队后再跳过」更省。

**备选与否决**:*入队后在 CLI 侧判角色*:多存多删续跑轮信号,浪费。否决,代理侧直接过滤。

## R6. Haiku 兜底走哪条链路?

**决策**:`HaikuClient` 把请求 POST 到**本网关自身**的 `/v1/messages`(`base_url` 默认指向本地网关端口),用一把配置里的内部 key,`model=claude-haiku-4-5-20251001`。

**理由**:
- 网关本就是 Anthropic `/v1/messages` 的旁路转发点,复用它即可享受既有的多后端负载均衡/计费/日志,无需分析器直连外部。
- 分析产生的 Haiku 调用本身也会被计费/记账,形成闭环可观测。

**风险与缓解**:分析器调用网关→网关转发 Haiku,可能再触发一条 `usage_logs`(且是成功 user_initiated,会再入 pending)。**缓解**:给分析器发出的请求带一个可识别标记(如特定 UA `claude-gateway-analyzer` 或专用 api key),代理侧信号抽取时识别该标记并**跳过入队**,避免分析自我递归。此细节在 data-model 的 pending 写入条件中固化。

**备选与否决**:
- *分析器直连外部 Anthropic*:绕开网关,多一套凭证与出口配置,且调用不计入网关账。否决。
- *structured outputs beta 强约束 JSON*:不依赖特定 beta,用 prompt 约束 + `stripFences` + 解析失败标记待重试(FR-010)。保留为后续可选增强。

## R7. 回写字段落点

**决策**:`usage_logs` 新增 3 列 `task_type TEXT`、`work_related INTEGER`(0/1,-1 或 NULL 表示未定)、`code_direction TEXT`;`work_reason` 与 `doc_activity` 拼接进既有 `error_reason` 列(成功请求该列通常为空,不冲突)。

**理由**:实现 FR-012/FR-013;最小化 schema 变更;低频文本复用现有列,避免为稀疏文本新增列。

**冲突检查**:`error_reason` 在**失败**请求上承载错误码;但失败请求不进分析队列(R4),故不会与 `work_reason` 争用同一行。成功请求的 `error_reason` 现状为空,可安全承载分析文本。用固定前缀区分,如 `work:<reason>;doc:<activity>`。

**备选与否决**:*新增两列文本*:多两列稀疏字段。否决(用户明确要求复用错误字段)。

## R8. 增量水位线 / 重复运行幂等

**决策**:`pending_analysis` 表本身即为「待分析水位线」——只存尚未分析的记录。`check --analyze` 每批 `SELECT ... WHERE retry_count < N ORDER BY id LIMIT 500`,回写成功即 `DELETE`。重复运行只会看到未删除的行,天然幂等(FR-014/FR-015、SC-005)。失败的记录留在表中、`retry_count++`,下次重试;超过上限的行标记跳过(留待人工/后续处理)。

**理由**:用「队列表 = 未处理集合」替代额外的 watermark 游标,简单且幂等。

**备选与否决**:*单独 watermark(last_analyzed_id)+ 保留全部信号*:信号不删则违反 FR-015a;且需额外游标管理。否决。

## R9. 聚合与评分数据源

**决策**:聚合直接查回写后的 `usage_logs`(已带 task_type/work_related/code_direction + created_at + user_id),按 `user_id` × 时间窗 GROUP BY 计算 Rollup 与 `AbuseScore`。复核队列 = 评分≥阈值的用户列表 + `error_reason` 里的 work_reason 抽样。

**理由**:`usage_logs` 已是持久事实源且已按 user/created_at 建索引(迁移 25、37-39 已就绪),无需额外聚合表。评分为读时计算,权重改了立即生效,便于校准(FR-022)。

**备选与否决**:*物化 rollup 表*:与既有 `daily_stats` 类似可后续加,但 v1 读时计算已够(数万行/天),避免过早优化。否决(YAGNI)。

## R10. 时区与非工作时段

**决策**:`offHours` 基于**网关本地时区**(进程 TZ)判定(默认 22:00–08:00 + 周末),阈值与时段边界可配置。

**理由**:实现 FR-022 与边界用例(spec Edge Cases 最后一条)。本地时区是单一确定基准,避免每用户时区推断。

**备选与否决**:*按用户 IP/城市推时区*:`city` 字段稀疏(需 `--ip2region` 事后解析),不可靠。否决,留作后续增强。
