# Claude Gateway

企业内部 Claude API 访问网关，提供统一的 API 入口、用户管理、配额控制和使用统计。

## 功能特性

- **API 兼容**：同时支持 OpenAI 风格（`/v1/chat/completions`）和 Anthropic 原生风格（`/v1/messages`）
- **多后端负载均衡**：加权随机分发，自动故障剔除与恢复，可选启动时健康检查
- **AWS Bedrock 支持**：独立渠道接入 AWS Bedrock，支持流式和非流式，自动格式转换，独立配额管理
- **第三方 Provider 支持**：配置 `public_providers` 接入任意兼容 OpenAI/Anthropic 协议的第三方服务
- **自动降级**：请求失败时自动降级到配置的 fallback 模型（由 `fallback` 配置项指定，路由到 public provider）
- **用户管理**：基于验证码 + 邀请码的注册登录，支持用户状态和配额管理
- **API Key 管理**：用户自助创建和管理 API Key，支持按渠道（backend/aws）分类
- **USD 配额控制**：按用户每日 USD 花费限额，分 backend 和 AWS 独立控制
- **使用统计**：记录每次请求的 Token 用量和费用，支持按用户/模型/日期查询
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
