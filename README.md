# Claude Gateway

企业内部 Claude API 访问网关，提供统一的 API 入口、用户管理、配额控制和使用统计。

## 功能特性

- **API 兼容**：同时支持 OpenAI 风格（`/v1/chat/completions`）和 Anthropic 原生风格（`/v1/messages`）
- **多后端负载均衡**：加权随机分发，自动故障剔除与恢复，启动时健康检查
- **用户管理**：基于验证码 + 邀请码的注册登录，支持用户状态和配额管理
- **API Key 管理**：用户自助创建和管理 API Key，支持过期时间设置
- **使用统计**：记录每次请求的 Token 用量，支持按用户/模型/日期查询
- **审批流程**：用户提交模型使用申请，管理员审批
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

backends:
  - name: claude-primary
    url: https://api.anthropic.com
    api_key: "sk-ant-xxx"
    weight: 10           # 权重，越高分配流量越多
    enabled: true
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

- **用户管理**：创建用户、修改角色/状态/Token 配额
- **申请审批**：审批或拒绝用户的模型使用申请
- **全局统计**：查看所有用户的用量数据

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

- 启动时对每个后端调用 `GET /v1/models` 验证可用性，失败的后端永久禁用（重启恢复）
- 运行时连续 5 次请求失败后临时禁用，30 秒后自动恢复

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
| 数据库 | SQLite (modernc.org/sqlite，无 CGO) |
| 日志 | Logrus |
| 前端 | React 19 + TypeScript + Tailwind CSS v4 |
| 构建 | Vite |
