# Claude Gateway

企业内部 Claude API 访问网关，提供统一的 API 入口、用户管理、配额控制和使用统计。

## 功能特性

- **API 兼容**：同时支持 OpenAI 风格（`/v1/chat/completions`）、Anthropic 原生风格（`/v1/messages`）和 OpenAI Responses API（`/v1/responses`，供 Codex CLI 使用）
- **Responses API 适配**：backend 渠道支持 `/v1/responses`，网关将其转换为 Anthropic Messages 协议落地，透传 function 工具、复用 WebSearch loop，支持流式 SSE，客户端无需上游改动即可接入 Codex
- **多后端负载均衡**：加权随机分发，自动故障剔除与恢复，可选启动时健康检查
- **AWS Bedrock 支持**：独立渠道接入 AWS Bedrock，支持流式和非流式，自动格式转换，独立配额管理
- **第三方 Provider 支持**：配置 `public_providers` 接入任意兼容 OpenAI/Anthropic 协议的第三方服务
- **自动降级**：请求失败时自动降级到配置的 fallback 模型（由 `fallback` 配置项指定，路由到 public provider）
- **用户管理**：基于验证码 + 邀请码的注册登录，支持用户状态和配额管理
- **API Key 管理**：用户自助创建和管理 API Key，支持按渠道（backend/aws）分类
- **USD 配额控制**：按用户每日 USD 花费限额，分 backend 和 AWS 独立控制
- **使用统计**：记录每次请求的 Token 用量和费用，支持按用户/模型/日期查询
- **IP 归属记录**：记录每次请求的客户端 IP、城市和是否公司总部（基于 CIDR 判定），城市通过本地缓存 + `check --ip2region` 异步解析
- **流量离线滥用分析**：对每条成功请求离线打标（任务类型 / 代码方向 / 是否工作相关），规则优先、Haiku 兜底，按人聚合出滥用评分与人工复核队列；分析走旁路，不影响转发链路，且原始请求正文不落库
- **WebSearch 联网搜索**：backend 渠道在网关层模拟 Anthropic 的 `web_search` server tool，接入 SearXNG 完成搜索并回填，向 Claude Code 输出原生格式，客户端无需改动即可联网
- **热重载**：发送 SIGHUP 信号即可重载配置，自动刷写数据后更新后端和规则
- **审批流程**：用户提交模型使用申请，管理员审批
- **DB Explorer**：管理员在线执行只读 SQL 查询，实时查看数据库内容
- **Web 管理后台**：React 前端，支持用户自助操作和管理员管理

## 快速开始

### 环境要求

- Go 1.23+
- Node.js 18+（构建前端）

### 1. 克隆并配置

```bash
git clone https://github.com/wjzhangq/claude-gateway
cd claude-gateway

cp config/config.example.yaml config/config.yaml
```

编辑 `config/config.yaml`，至少需要配置：

```yaml
auth:
  session_secret: "your-random-secret"  # openssl rand -hex 32
  admin_itcode: "your-admin-account"

backends:
  - name: claude-primary
    url: https://api.anthropic.com
    api_key: "sk-ant-xxx"
    weight: 10
    enabled: true
```

### 2. 构建

```bash
bash scripts/build.sh
```

构建产物：`bin/gateway`（单一可执行文件）

### 3. 运行

```bash
./bin/gateway
```

访问 `http://localhost:8080` 打开管理后台。

默认端口 8080，可在配置文件中修改。

---

## 配置说明

配置文件路径默认为 `config/config.yaml`，可通过环境变量覆盖：

```bash
CONFIG_PATH=/etc/claude-gateway/config.yaml ./bin/gateway
```

### 完整配置项

```yaml
server:
  port: 8080
  mode: release          # debug / release

database:
  path: data/gateway.db  # SQLite 文件路径，自动创建

log:
  level: info            # debug / info / warn / error
  format: json           # json / text

auth:
  session_secret: ""     # Cookie 签名密钥，必填，建议 openssl rand -hex 32
  session_max_age: 86400 # Session 有效期（秒），默认 24 小时
  code_expiry: 5m        # 验证码有效期
  admin_itcode: ""       # 首次启动自动创建的管理员账号
  send_code_url: ""      # 发送验证码的外部 HTTP 接口（为空时验证码打印到日志）
  invite_code: ""        # 注册邀请码（为空时不校验）

usage_sync_time: 5m      # 用量聚合到 daily_stats 的间隔

downgraded_ttl: 60s      # 降级窗口时长：触发降级后该 Key 在此时间内持续走 fallback 模型

fallback: ""             # 自动降级目标模型名，需在 public_providers 中配置对应模型

backends:
  - name: claude-primary
    url: https://api.anthropic.com
    api_key: "sk-ant-xxx"
    weight: 10           # 权重，越高分配流量越多
    enabled: true

validate_backends: false   # 启动时是否检查后端 /v1/models 可用性（默认 false）

model_replacements:      # 模型名称替换规则（客户端请求的模型名 -> 实际转发的模型名）
  claude-3-5-sonnet: claude-3-5-sonnet-20241022

public_providers:        # 第三方兼容服务（可用于 fallback 或直接路由）
  - name: openai
    openai_url: https://api.openai.com/v1
    api_key: "sk-xxx"
    enabled: true
    models:
      - gpt-4o
      - gpt-4o-mini

aws:
  region: us-east-1
  access_key_id: ""
  secret_access_key: ""

ip_geo:                        # 请求 IP → 城市 / 是否总部记录（backend + public 渠道）
  cache_file: data/ip2region.json  # IP → 城市本地缓存文件，服务器独占读写
  hq_cidrs:                    # 命中任一网段的 IP 标记为公司总部（is_hq=true）
    - 111.205.0.0/27
```

### send_code_url 接口规范

如果配置了 `send_code_url`，网关会向该地址发送 POST 请求：

```json
{
  "email": "user@example.com",
  "html": "<验证码邮件 HTML 内容>"
}
```

响应 2xx 视为发送成功。未配置时，验证码会打印到日志（适合开发调试）。

---

## API 使用

### 认证方式

代理接口使用 API Key 认证，支持两种方式：

```
Authorization: Bearer sk-xxxxxxxx
```
或
```
x-api-key: sk-xxxxxxxx
```

### 代理接口

| 接口 | 说明 |
|------|------|
| `POST /v1/chat/completions` | OpenAI 兼容接口 |
| `POST /v1/messages` | Anthropic 原生接口 |
| `POST /v1/responses` | OpenAI Responses API（Codex CLI），仅 backend 渠道，详见下文 |
| `GET /v1/models` | 获取可用模型列表 |

**示例（OpenAI 风格）：**

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-your-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-3-5-sonnet-20241022",
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```

**示例（Anthropic 风格）：**

```bash
curl http://localhost:8080/v1/messages \
  -H "Authorization: Bearer sk-your-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-3-5-sonnet-20241022",
    "max_tokens": 1024,
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```

支持流式响应（SSE），在请求体中加 `"stream": true` 即可。

启用 `websearch` 后，backend 渠道的 `/v1/messages` 请求若声明了 Anthropic 的 `web_search` server tool，网关会自动接入 SearXNG 完成联网搜索（详见下文「WebSearch 联网搜索」）。

---

## 管理后台

访问 `http://localhost:8080` 进入 Web 管理后台。

### 用户功能

- **登录**：输入账号 + 邀请码，接收验证码后登录
- **API Key 管理**：创建、查看、禁用、删除 API Key
- **使用统计**：查看自己的 Token 用量和请求记录
- **模型申请**：提交模型使用申请，等待管理员审批

### 管理员功能

- **用户管理**：创建用户、修改角色/状态/配额，启用/禁用 AWS 渠道权限
- **申请审批**：审批或拒绝用户的模型使用申请
- **全局统计**：查看所有用户的用量数据
- **DB Explorer**：在线执行只读 SQL 查询（底层使用只读连接，无法执行写操作）

---

## 自动降级

当请求后端失败（网络错误或 HTTP 4xx/5xx）且 API Key 开启了 `auto_downgrade` 时，网关会自动将请求转发到 `fallback` 配置的模型。

```yaml
fallback: gpt-4o-mini   # 降级目标，必须在 public_providers 的 models 中
downgraded_ttl: 60s     # 触发一次降级后，该 Key 后续请求持续走 fallback 的时长
```

降级行为：

1. 原始请求失败 → 替换模型名 → 转发到 fallback 模型所在的 public provider
2. 降级成功后设置 TTL，TTL 期间内该 Key 的后续请求直接走 fallback，避免反复尝试失败的后端
3. TTL 到期后恢复尝试原始后端

---

## 龙虾流量自动转发

对于被识别为龙虾客户端（OpenClaw / Hermes）的请求，当其请求 Claude 模型时，可以配置自动转发到 fallback 模型，而非直接阻断。

```yaml
# 开关：true 时龙虾请求 Claude 模型自动转发到 fallback
lobster_auto_forward: true

# 白名单用户（itcode）：白名单中的用户不做转发，正常走 backend 路由
lobster_forward_whitelist:
  - "zhangsan"
  - "lisi"
```

行为规则：

| 条件 | 行为 |
|------|------|
| 龙虾 + Claude 模型 + 开关开 + 非白名单 | 自动转发到 fallback 模型（如 MiniMax-M2.7） |
| 龙虾 + Claude 模型 + 开关关 + 非白名单 | 返回 403 阻断 |
| 龙虾 + Claude 模型 + 白名单用户 | 正常走 backend（不拦截、不转发） |
| 龙虾 + 非 Claude 模型 | 正常放行 |

转发后的请求会被标记为 `is_downgraded`，统计中可区分正常请求和转发请求。

---

## AWS Bedrock

为用户开启 `aws_enabled` 后，该用户可创建 `channel=aws` 的 API Key，请求自动路由到 AWS Bedrock。

```yaml
aws:
  region: us-east-1
  access_key_id: "AKIA..."
  secret_access_key: "..."
```

支持的接口与普通代理相同（`/v1/chat/completions`、`/v1/messages`），网关负责协议转换。AWS 用量独立统计，可配置独立的每日 USD 配额（`aws_daily_quota_usd`）。

---

## 负载均衡

支持配置多个后端，按权重随机分发流量：

```yaml
backends:
  - name: backend-1
    url: https://api.anthropic.com
    api_key: "sk-ant-key1"
    weight: 10
    enabled: true
  - name: backend-2
    url: https://api.anthropic.com
    api_key: "sk-ant-key2"
    weight: 5
    enabled: true
```

**健康检查机制：**

- 启动时健康检查默认关闭，设置 `validate_backends: true` 后启用（对每个后端调用 `GET /v1/models` 验证可用性，失败的后端永久禁用，重启恢复）
- 运行时连续 5 次请求失败后临时禁用，30 秒后自动恢复

---

## 请求 IP / 城市 / 是否总部记录

backend 和 public 渠道的每次请求都会记录来源 IP、所属城市和是否公司总部（`usage_logs` 表的 `ip` / `city` / `is_hq` 字段）。AWS 渠道不在记录范围内。

```yaml
ip_geo:
  cache_file: data/ip2region.json  # IP → 城市本地缓存文件
  hq_cidrs:                        # 命中任一网段即标记 is_hq=true
    - 111.205.43.224/27
    - 111.198.161.0/24             # VPN 网段也算总部
```

工作机制：

1. **是否总部**：请求 IP 落在 `hq_cidrs` 任一网段内则 `is_hq=true`，无需外部查询，实时判定。
2. **城市**：网关维护一个进程内 IP → 城市缓存（周期性落盘到 `cache_file`），命中即回填城市。首次见到的 IP 城市为空，等待离线补全；内网 / 环回 / 保留地址不记录城市。
3. **补全城市**：运行 `check --ip2region`，从网关拉取所有「城市为空」的公网 IP，逐个通过 [ip9.com.cn](https://ip9.com.cn) 查询城市（免 key），再回传给网关更新缓存。下次同 IP 的请求记录即可带上城市。

```bash
# 补全尚未解析城市的 IP（网关需在运行中）
./bin/check --ip2region
```

建议配合 cron 定期执行（如每小时一次），让新出现的 IP 逐步补全城市。缓存文件由网关进程独占读写，`check` 只通过 HTTP 管理接口（`session_secret` 鉴权）与网关交互，避免多进程并发写文件。

---

## 流量离线滥用分析

对每条**成功**转发的 Claude Code 请求离线打标（任务类型 / 代码方向 / 是否工作相关），并按人聚合出滥用评分与人工复核队列。**只识别，不自动处罚。**

设计要点：

- **不阻塞热路径**：信号抽取在响应回写客户端**之后**、代理协程内完成，只处理内存中已有的请求正文，转发链路 P99 延迟无可测量增加。
- **原始正文不落库**：持久层只存**压缩信号**（意图截断 ≤300 字 + 文件名 / 命中仓库 / 命令首动词 / 工具名），绝不含 system prompt、工具 schema、历史回复或文件正文；分析回写后即删信号。
- **规则优先、Haiku 兜底**：后缀投票定代码方向、命中内部仓库判工作相关；规则拿不准才调用 Haiku（经本网关自身 `/v1/messages`，被计费与记账）。工具续跑 / 子代理轮不计逻辑任务、不调模型。
- **只分析成功请求**：仅 `status_code < 400` 且为 `user_initiated` 的请求入待分析队列。

```yaml
analyze:
  enabled: true
  haiku_base_url: "http://127.0.0.1:8080"   # 默认指向本网关自身
  haiku_api_key: "<内部分析专用网关 key>"     # 留空则纯规则模式，不做 Haiku 兜底
  haiku_model: "claude-haiku-4-5-20251001"
  analyzer_ua: "claude-gateway-analyzer"      # 代理据此识别分析器自身请求并跳过入队（防自递归）
  batch_size: 500
  max_retry: 3
  score:                                       # 滥用评分权重（读时计算，改后即时生效）
    non_work: 0.6
    off_hours: 0.15
    volume: 0.25
    baseline_tasks: 60
    threshold: 0.5                             # 评分 ≥ 阈值进复核队列
```

工作机制：

1. **采集**：代理侧对成功的 `user_initiated` 请求抽取压缩信号，与 `usage_logs` 行在**同一事务**内写入 `pending_analysis` 队列（失败请求照常记账但不入队）。
2. **分析**：运行 `check --analyze`，从网关分批拉取待分析队列，逐条规则分类（拿不准调 Haiku），结论回写 `usage_logs`（新增 `task_type` / `work_related` / `code_direction` 三列，理由复用 `error_reason`），回写成功即删除队列行。单条 Haiku 失败只标记重试、不中断整批。
3. **报表**：`GET /admin/api/insight/abuse?window=day|week|month` 返回每人画像 + 滥用评分 + 复核队列（评分 ≥ 阈值，附非工作原因抽样）。

```bash
# 消费待分析队列并回写结论（网关需在运行中）
./bin/check --analyze
```

`enabled: false`（默认）时代理侧完全跳过信号抽取与入队，零开销。与 `--ip2region` 一样，`check --analyze` 只通过 `session_secret` 鉴权的 HTTP 管理接口与网关交互，不直接开库，避免争抢 SQLite 写锁。建议配合 cron 定期执行。

---

## WebSearch 联网搜索

Claude Code 的 WebSearch 依赖 Anthropic 的 server tool `web_search_20250305`：客户端只声明工具，搜索由 Anthropic 服务端执行。**backend 渠道**的上游是第三方 Anthropic 兼容中转，不支持该 server tool，请求会直接报错。网关在其间做一层模拟，让 Claude Code 无需任何改动即可联网搜索并原生渲染搜索过程。

**仅作用于 backend 渠道**（`/v1/messages`）。AWS / public providers 不涉及。

```yaml
websearch:
  enabled: true
  provider: searxng
  search_url: "https://searxng.example.com/search"
  authorization: "Bearer <token>"      # 原样放入 Authorization 头
  language: "zh-CN"                     # 默认语言，可被请求覆盖
  max_results: 8                        # 每次搜索返回给模型的条数
  snippet_max_chars: 800                # 每条结果摘要截断长度
  timeout: 10s                          # SearXNG 请求超时
  default_max_uses: 5                   # 请求未指定 max_uses 时的单请求搜索上限
```

工作机制（agent loop）：

1. **改写请求**：检测到请求 `tools` 里带 `web_search_` 前缀的 server tool 时，从 tools 中移除它，注入一个普通 client tool `web_search`（`{query}` 入参）转发上游；历史消息里网关此前产生的 `server_tool_use` / `web_search_tool_result` 块会被还原成标准 `tool_use` / `tool_result`，让上游能识别多轮上下文。
2. **执行搜索**：上游模型返回 `web_search` 调用时，网关拦截，调用 SearXNG（`?q=&format=json&language=`，`Authorization` 头透传），取结果按 score 排序、截断摘要，并按请求携带的 `allowed_domains` / `blocked_domains` 过滤域名。
3. **回填续跑**：把搜索结果作为 `tool_result` 回填，继续请求上游，循环直到模型不再搜索、调用了客户端工具，或达到 `max_uses`（超限回填 `max_uses_exceeded`，让模型基于已有信息收尾）。
4. **原生输出**：向 Claude Code 输出 Anthropic 原生的 `server_tool_use` + `web_search_tool_result` 块，客户端原生渲染。usage 聚合多轮上游的 input/output tokens，并填 `usage.server_tool_use.web_search_requests`。

流式说明：agent loop 对上游一律走非流式（多轮调用）；对客户端，非流式请求直接返回完整 JSON，流式请求（`stream:true`）由网关合成标准 SSE 事件序列（`message_start → content_block_* → message_delta → message_stop`），搜索期间穿插 `ping` 事件防客户端超时。搜索失败不会 500 整个请求，而是回填 `web_search_tool_result_error{error_code:"unavailable"}`，让模型降级作答。

`enabled: false`（默认）时代理完全不拦截 web_search，请求按原路透传。配置可随 SIGHUP 热重载（见下）。

---

## Responses API（Codex CLI）

Codex CLI 走 OpenAI 的 Responses 协议（`POST /v1/responses`），与 Chat Completions / Messages 都不同。网关在 **backend 渠道** 支持该协议：把 Responses 请求转换成 Anthropic Messages 落地到上游，再把上游结果转换回 Responses 输出。**AWS 渠道不支持**（返回 404）。

选择 Anthropic Messages 作为落地协议的原因：backend 渠道上游本就是 Anthropic 兼容中转，转成 Chat Completions 反而要多一跳；WebSearch loop 也是 Anthropic 原生的，可以直接复用。

**协议映射：**

| Responses 构造 | Anthropic 落地 |
|------|------|
| `instructions` / system、developer 消息 | 顶层 `system` |
| `input` 消息（字符串或 `input_text` / `output_text`） | `user` / `assistant` 文本块 |
| `function_call` | assistant `tool_use` 块 |
| `function_call_output` | user `tool_result` 块 |
| `tools` 里的 `function` | Anthropic 工具（`input_schema`） |
| `tools` 里的 `web_search` | 注入 Anthropic web_search server tool，走 WebSearch loop |
| `web_search_call` / `reasoning` 历史项 | 丢弃（信息已在后续正文中） |
| 上游 `server_tool_use` + `web_search_tool_result` | `web_search_call` 输出项 |
| 上游文本块 | `message` / `output_text` 输出项 |
| `stop_reason: max_tokens` | `status: incomplete` + `incomplete_details.max_output_tokens` |

**工作机制：**

1. **转换请求**：解码 Responses 请求，映射为 Anthropic Messages（含 function 工具与可选 web_search）。
2. **复用 loop**：交给与 WebSearch 共享的协议无关 agent loop。声明了 `web_search` 时走多轮搜索续跑；只有 function 工具时退化为单轮普通请求（function 工具由 Codex 客户端本地执行，网关只透传声明并中转 `function_call` / `function_call_output`）。
3. **转换响应**：把上游内容块转换回 Responses `output[]`（`message` / `web_search_call` / `function_call`），聚合 usage，`function_call` 会带出 `stop_reason: tool_use`，由 Codex 执行后回传。

**无状态：** 网关不保存任何 per-response 状态——`previous_response_id` 直接返回 400，`store` 恒为 `false`。Codex 需按全量历史模式配置（每轮发送完整 `input[]`）。

**流式：** `stream: true` 时网关合成标准 Responses SSE 事件序列（`response.created → response.output_item.added → response.web_search_call.* / response.output_text.delta / response.function_call_arguments.delta → response.output_item.done → response.completed`），每个事件带单调递增的 `sequence_number`，搜索期间穿插 keep-alive 保活。

**示例：**

```bash
curl http://localhost:8080/v1/responses \
  -H "Authorization: Bearer sk-your-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-opus-4-8",
    "input": "search the web and summarize the latest Go release",
    "stream": true,
    "tools": [{"type": "web_search"}]
  }'
```

web_search 增强依赖 `websearch.enabled: true`（见上文）；关闭时若请求带 `web_search` 工具，网关会剥除该 server tool 后正常转发，其余 function 工具不受影响。

---

## 热重载

支持通过 `SIGHUP` 信号触发配置热重载，无需重启服务。

```bash
# 查找进程 PID
pgrep -f gateway

# 发送 SIGHUP 信号
kill -HUP <pid>
```

**重载流程：**

1. 将内存中待写入的 usage 记录刷入数据库
2. 立即执行一次日统计聚合
3. 重新读取并校验配置文件（失败则保留旧配置，不影响服务）
4. 热更新后端列表（重建连接并重新验证）、模型替换映射、分组配置
5. 重新加载 API Key 缓存

**可热更新的配置项：**

- `backends` — 后端列表（增删改、权重调整）
- `model_replacements` — 模型名称替换规则
- `public_providers` — 第三方 Provider 列表
- `fallback` — 自动降级目标模型
- `lobster_auto_forward` — 龙虾自动转发开关
- `lobster_forward_whitelist` — 龙虾转发白名单
- `groups` — 用户分组
- `analyze` — 离线滥用分析配置（评分权重 / 阈值 / 时间窗 / 内部仓库 / 重试上限）
- `websearch` — WebSearch 联网搜索配置（开关 / SearXNG 地址 / 语言 / 结果数 / 超时 / max_uses）

**不可热更新的配置项（需重启）：**

- `server.port` — 监听端口
- `database.path` — 数据库路径
- `auth.session_secret` — Session 密钥

配合 systemd 使用时，可通过 `systemctl reload` 触发：

```ini
[Service]
ExecReload=/bin/kill -HUP $MAINPID
```

---

## 部署

### 直接部署

```bash
# 构建
bash scripts/build.sh

# 运行（确保 config/config.yaml 和 web/dist/ 在当前目录）
./bin/gateway
```

### 目录结构（运行时）

```
./
├── bin/gateway          # 可执行文件
├── config/config.yaml   # 配置文件
├── data/gateway.db      # SQLite 数据库（自动创建）
└── web/dist/            # 前端静态资源
```

### 使用 systemd

```ini
[Unit]
Description=Claude Gateway
After=network.target

[Service]
ExecStart=/opt/claude-gateway/bin/gateway
WorkingDirectory=/opt/claude-gateway
Restart=on-failure
Environment=CONFIG_PATH=/opt/claude-gateway/config/config.yaml

[Install]
WantedBy=multi-user.target
```

---

## 开发

```bash
# 后端开发模式
go run cmd/server/main.go

# 前端开发模式（代理到 localhost:8080）
cd web && npm install && npm run dev

# 运行测试
go test ./...

# 仅构建后端
go build -o bin/gateway ./cmd/server
```

## 技术栈

| 组件 | 技术 |
|------|------|
| 后端 | Go 1.23+, Gin |
| 数据库 | SQLite (modernc.org/sqlite，无 CGO，WAL 模式) |
| 日志 | Logrus |
| 前端 | React 19 + TypeScript + Tailwind CSS v4 |
| 构建 | Vite |

## 数据库迁移

采用内置版本化迁移，无需外部工具。启动时自动创建 `schema_migrations` 表，按版本号顺序执行未应用的迁移，已执行过的版本自动跳过，支持增量升级。
