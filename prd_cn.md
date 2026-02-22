# Claude Gateway 产品需求文档

> 版本：1.0  
> 日期：2026-02-22  
> 语言：Go 1.23+ / React 19 + TypeScript

---

## 1. 项目概述

Claude Code Gateway 是一个企业内部统一的 Claude 模型访问网关，旨在为企业内部用户提供安全、可控、可审计的 Claude API 访问能力。

核心目标：
- 统一管理企业内部对 Claude 模型的访问入口
- 基于邀请码 + 企业邮箱验证码的身份认证体系
- API Key 管理与配额控制
- 多后端负载均衡与故障自动恢复
- 完整的用量统计与审计日志
- 模型申请审批流程

---

## 2. 技术栈

| 层次 | 技术选型 |
|------|----------|
| 后端语言 | Go 1.23+ |
| Web 框架 | Gin |
| 数据库 | SQLite（modernc.org/sqlite，纯 Go 实现，无 CGO 依赖） |
| 日志 | Logrus |
| 配置 | YAML |
| 前端框架 | React 19 + TypeScript |
| 前端样式 | Tailwind CSS v4 |
| 前端构建 | Vite |
| 前端路由 | React Router v7 |
| 前端 HTTP | Axios |

---

## 3. 系统架构

```
┌─────────────────────────────────────────────────────────┐
│                      客户端 / 前端                        │
│              React 19 + TypeScript + Vite                │
└───────────────────────────┬─────────────────────────────┘
                            │ HTTP
┌───────────────────────────▼─────────────────────────────┐
│                    Gin HTTP Server                        │
│  ┌─────────────┐  ┌──────────────┐  ┌────────────────┐  │
│  │  Auth 中间件 │  │ 限流中间件    │  │  日志中间件     │  │
│  └─────────────┘  └──────────────┘  └────────────────┘  │
│                                                          │
│  ┌──────────────────────────────────────────────────┐   │
│  │                   路由层                           │   │
│  │  /api/auth/*   /api/keys   /api/usage             │   │
│  │  /api/applications         /admin/api/*           │   │
│  │  /v1/chat/completions  /v1/messages  /v1/models   │   │
│  └──────────────────────────────────────────────────┘   │
│                                                          │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐   │
│  │  Handler 层  │  │  Proxy 层    │  │  Stats 层    │   │
│  │  auth.go     │  │  handler.go  │  │  collector   │   │
│  │  user.go     │  │  balancer.go │  │  aggregator  │   │
│  │  apikey.go   │  └──────┬───────┘  └──────┬───────┘   │
│  │  stats.go    │         │                  │           │
│  │  application │         │                  │           │
│  └──────────────┘         │                  │           │
│                           │                  │           │
│  ┌──────────────┐  ┌──────▼───────┐  ┌──────▼───────┐   │
│  │  Auth 层     │  │  负载均衡器   │  │  SQLite DB   │   │
│  │  key_store   │  │  加权随机     │  │  5 张表      │   │
│  │  code_store  │  │  连接池       │  │              │   │
│  └──────────────┘  └──────┬───────┘  └──────────────┘   │
└───────────────────────────┼─────────────────────────────┘
                            │ HTTP
┌───────────────────────────▼─────────────────────────────┐
│              Claude API 后端集群                          │
│         Backend 1 / Backend 2 / Backend N                │
└─────────────────────────────────────────────────────────┘
```

---

## 4. 项目目录结构

```
claude-gateway/
├── cmd/
│   └── server/
│       └── main.go                 # 程序入口
├── config/
│   ├── config.go                   # 配置结构体与加载逻辑
│   ├── config.yaml                 # 实际配置文件（不提交）
│   └── config.example.yaml         # 配置示例文件
├── internal/
│   ├── auth/
│   │   ├── key_store.go            # API Key 内存存储（sync.RWMutex）
│   │   └── code_store.go           # 验证码内存存储
│   ├── db/
│   │   ├── db.go                   # 数据库初始化与 schema 迁移
│   │   ├── user.go                 # 用户 CRUD
│   │   ├── application.go          # 申请单 CRUD
│   │   ├── daily_stats.go          # 每日统计聚合
│   │   └── stats.go                # 用量日志写入
│   ├── handler/
│   │   ├── auth.go                 # 认证相关接口
│   │   ├── user.go                 # 用户管理接口
│   │   ├── apikey.go               # API Key 管理接口
│   │   ├── stats.go                # 用量查询接口
│   │   └── application.go          # 申请单接口
│   ├── logger/
│   │   └── logger.go               # Logrus 初始化
│   ├── middleware/
│   │   ├── auth.go                 # Session 认证 / API Key 认证中间件
│   │   ├── logger.go               # 请求日志中间件
│   │   └── ratelimit.go            # 限流中间件
│   ├── model/
│   │   └── model.go                # 数据模型定义
│   ├── proxy/
│   │   ├── handler.go              # 反向代理处理器
│   │   └── balancer.go             # 加权随机负载均衡器
│   └── stats/
│       ├── collector.go            # 异步用量收集器（buffered channel）
│       └── aggregator.go           # 定时聚合器
├── web/
│   └── src/
│       ├── api.ts                  # Axios 封装与 API 调用
│       ├── App.tsx                 # 路由配置
│       ├── context/
│       │   └── AuthContext.tsx     # 全局认证状态
│       ├── components/             # 公共组件
│       └── pages/                  # 页面组件
│           ├── Login.tsx
│           ├── Dashboard.tsx
│           ├── ApiKeys.tsx
│           ├── Usage.tsx
│           ├── Applications.tsx
│           ├── admin/
│           │   ├── Users.tsx
│           │   ├── Applications.tsx
│           │   └── Usage.tsx
├── scripts/
│   └── build.sh                    # 一键构建脚本
└── data/                           # 运行时自动创建，存放 SQLite 文件
```

---

## 5. 配置结构

配置文件路径默认为 `config/config.yaml`，可通过环境变量 `CONFIG_PATH` 覆盖。

### 5.1 配置结构体（config/config.go）

```go
type Config struct {
    Server   ServerConfig   `yaml:"server"`
    Database DatabaseConfig `yaml:"database"`
    Log      LogConfig      `yaml:"log"`
    Auth     AuthConfig     `yaml:"auth"`
    Backends []BackendAPI   `yaml:"backends"`
    UsageSync time.Duration `yaml:"usage_sync"`
}

type ServerConfig struct {
    Port int    `yaml:"port"`   // 监听端口，默认 8080
    Mode string `yaml:"mode"`   // gin 模式：debug / release
}

type DatabaseConfig struct {
    Path string `yaml:"path"`   // SQLite 文件路径，如 data/gateway.db
}

type LogConfig struct {
    Level  string `yaml:"level"`   // 日志级别：debug/info/warn/error
    Format string `yaml:"format"`  // 日志格式：json / text
}

type AuthConfig struct {
    SessionSecret string        `yaml:"session_secret"` // Cookie 签名密钥
    SessionMaxAge int           `yaml:"session_max_age"`// Session 有效期（秒）
    CodeExpiry    time.Duration `yaml:"code_expiry"`    // 验证码有效期
    AdminItcode   string        `yaml:"admin_itcode"`   // 初始管理员 itcode
    SendCodeURL   string        `yaml:"send_code_url"`  // 发送验证码的外部 HTTP 接口
    InviteCode    string        `yaml:"invite_code"`    // 邀请码
}

type BackendAPI struct {
    Name    string  `yaml:"name"`    // 后端名称
    URL     string  `yaml:"url"`     // 后端 API 地址
    APIKey  string  `yaml:"api_key"` // 后端 API Key
    Weight  int     `yaml:"weight"`  // 负载均衡权重
    Enabled bool    `yaml:"enabled"` // 是否启用
}
```

### 5.2 配置示例（config/config.example.yaml）

```yaml
server:
  port: 8080
  mode: release          # debug 或 release

database:
  path: data/gateway.db  # SQLite 数据库文件路径

log:
  level: info            # debug / info / warn / error
  format: json           # json / text

auth:
  session_secret: "your-secret-key-change-this"
  session_max_age: 86400          # 24 小时（秒）
  code_expiry: 10m                # 验证码有效期 10 分钟
  admin_itcode: "admin"           # 初始管理员账号（itcode）
  send_code_url: "https://your-mail-service/send"  # 发送邮件的外部接口
  invite_code: "your-invite-code" # 注册邀请码

usage_sync: 5m                    # 用量聚合间隔，默认 5 分钟

backends:
  - name: "claude-primary"
    url: "https://api.anthropic.com"
    api_key: "sk-ant-xxxx"
    weight: 10
    enabled: true
  - name: "claude-secondary"
    url: "https://api.anthropic.com"
    api_key: "sk-ant-yyyy"
    weight: 5
    enabled: true
```

---

## 6. 数据库 Schema

数据库使用 SQLite，程序启动时自动创建 `data/` 目录并执行 schema 迁移。共 5 张表：

### 6.1 users — 用户表

```sql
CREATE TABLE IF NOT EXISTS users (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    itcode     TEXT NOT NULL UNIQUE,        -- 企业工号/账号
    name       TEXT NOT NULL,               -- 显示名称
    role       TEXT NOT NULL DEFAULT 'user',-- 角色：admin / user
    status     TEXT NOT NULL DEFAULT 'active', -- 状态：active / disabled
    quota_tokens INTEGER NOT NULL DEFAULT 0,-- Token 配额（0 表示无限制）
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

### 6.2 api_keys — API Key 表

```sql
CREATE TABLE IF NOT EXISTS api_keys (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER NOT NULL REFERENCES users(id),
    key        TEXT NOT NULL UNIQUE,        -- API Key 值
    name       TEXT NOT NULL,               -- Key 名称
    status     TEXT NOT NULL DEFAULT 'active', -- 状态：active / disabled
    expires_at DATETIME,                    -- 过期时间（NULL 表示永不过期）
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

### 6.3 usage_logs — 用量日志表

```sql
CREATE TABLE IF NOT EXISTS usage_logs (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER NOT NULL REFERENCES users(id),
    api_key_id   INTEGER NOT NULL REFERENCES api_keys(id),
    model        TEXT NOT NULL,             -- 模型名称
    backend      TEXT NOT NULL,             -- 后端名称
    input_tokens  INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    total_tokens  INTEGER NOT NULL DEFAULT 0,
    cost         REAL NOT NULL DEFAULT 0,   -- 费用（美元）
    status_code  INTEGER NOT NULL,          -- HTTP 状态码
    latency_ms   INTEGER NOT NULL,          -- 请求延迟（毫秒）
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

### 6.4 daily_stats — 每日统计表

```sql
CREATE TABLE IF NOT EXISTS daily_stats (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    date     TEXT NOT NULL,                 -- 日期，格式 YYYY-MM-DD
    user_id  INTEGER NOT NULL REFERENCES users(id),
    model    TEXT NOT NULL,
    requests INTEGER NOT NULL DEFAULT 0,
    tokens   INTEGER NOT NULL DEFAULT 0,
    cost     REAL NOT NULL DEFAULT 0,
    UNIQUE(date, user_id, model)            -- 按日期+用户+模型聚合
);
```

### 6.5 applications — 模型申请表

```sql
CREATE TABLE IF NOT EXISTS applications (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     INTEGER NOT NULL REFERENCES users(id),
    model       TEXT NOT NULL,              -- 申请使用的模型
    reason      TEXT NOT NULL,              -- 申请理由
    status      TEXT NOT NULL DEFAULT 'pending', -- 状态：pending / approved / rejected
    reviewer_id INTEGER REFERENCES users(id),   -- 审批人 ID
    review_note TEXT,                       -- 审批备注
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

---

## 7. API 路由

### 7.1 认证接口（无需登录）

| 方法 | 路径 | 限流 | 说明 |
|------|------|------|------|
| POST | `/api/auth/send-code` | 10次/分钟 | 发送验证码，需要 itcode + invite_code |
| POST | `/api/auth/login` | - | 登录，需要 itcode + code + invite_code |
| POST | `/api/auth/logout` | - | 登出，清除 Session |

**send-code 请求体：**
```json
{
  "itcode": "zhangsan",
  "invite_code": "your-invite-code"
}
```

**login 请求体：**
```json
{
  "itcode": "zhangsan",
  "code": "123456",
  "invite_code": "your-invite-code"
}
```

### 7.2 用户接口（Session 认证）

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/keys` | 获取当前用户的 API Key 列表 |
| POST | `/api/keys` | 创建新 API Key |
| PUT | `/api/keys/:id` | 更新 API Key（名称/状态） |
| DELETE | `/api/keys/:id` | 删除 API Key |
| GET | `/api/usage` | 获取当前用户用量统计 |
| POST | `/api/applications` | 提交模型申请 |
| GET | `/api/applications` | 获取当前用户的申请列表 |

### 7.3 管理员接口（Session 认证 + admin 角色）

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/admin/api/users` | 获取所有用户列表 |
| GET | `/admin/api/users/:id` | 获取指定用户详情 |
| POST | `/admin/api/users` | 创建用户 |
| PUT | `/admin/api/users/:id` | 更新用户信息（角色/状态/配额） |
| GET | `/admin/api/usage` | 获取全局用量日志 |
| GET | `/admin/api/usage/daily` | 获取每日聚合统计 |
| GET | `/admin/api/applications` | 获取所有申请单 |
| PUT | `/admin/api/applications/:id/review` | 审批申请单 |

### 7.4 代理接口（API Key 认证）

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/v1/chat/completions` | OpenAI 兼容的对话接口 |
| POST | `/v1/messages` | Anthropic 原生消息接口 |
| GET | `/v1/models` | 获取可用模型列表 |

---

## 8. 认证流程

### 8.1 验证码登录流程

```
用户输入 itcode + invite_code
        │
        ▼
POST /api/auth/send-code
        │
        ├── 校验 invite_code 是否匹配配置
        │
        ├── itcode 不含 @ 则自动追加 @lenovo.com
        │   （如 "zhangsan" → "zhangsan@lenovo.com"）
        │
        ├── 生成 6 位随机验证码，存入 code_store（内存）
        │
        └── HTTP POST 到 send_code_url
            Body: { "email": "zhangsan@lenovo.com", "html": "<验证码邮件内容>" }

用户收到邮件，输入验证码
        │
        ▼
POST /api/auth/login
        │
        ├── 再次校验 invite_code
        ├── 从 code_store 验证 code 是否正确且未过期
        ├── 查询或自动创建用户记录
        └── 写入 signed cookie（Session）
```

### 8.2 API Key 认证流程

```
请求携带 Authorization: Bearer sk-xxx
        │
        ▼
middleware/auth.go（API Key 中间件）
        │
        ├── 从 KeyStore（内存，sync.RWMutex）O(1) 查找
        │   KeyStore 在程序启动时从数据库全量加载
        │
        ├── 校验 Key 状态（active）
        ├── 校验过期时间
        └── 注入 user_id 到 Gin Context
```

---

## 9. 负载均衡器

### 9.1 设计原则

负载均衡器（`internal/proxy/balancer.go`）采用加权随机算法，为每个后端维护独立的 HTTP 连接池。

### 9.2 后端选择算法

```
所有 enabled=true 且未被禁用的后端
        │
        ▼
按 weight 计算总权重
        │
        ▼
生成 [0, totalWeight) 随机数
        │
        ▼
遍历后端列表，累加权重，找到命中的后端
```

### 9.3 错误追踪与自动恢复

- 每个后端维护连续错误计数器
- 连续错误达到 5 次 → 后端被临时禁用
- 禁用 30 秒后自动恢复，重新参与负载均衡

### 9.4 启动验证

程序启动时对每个 `enabled=true` 的后端执行验证：

```
GET /v1/models（携带对应 api_key）
        │
        ├── 成功且 data 数组非空 → 验证通过
        │
        └── 失败 → validationFailed = true（atomic.Bool）
            永久标记，不会自动恢复
            该后端不参与任何请求转发
```

> 注意：启动验证失败是永久性的，与运行时错误追踪的 30 秒自动恢复机制相互独立。

### 9.5 HTTP 连接池

每个后端使用独立的 `http.Transport` 实例，避免连接池竞争，提升并发性能。

---

## 10. 用量统计流程

```
代理请求完成
        │
        ▼
stats/collector.go
        │
        ├── 将用量数据写入 buffered channel（容量 1024）
        │   （非阻塞，channel 满时丢弃并记录警告日志）
        │
        └── 后台 goroutine 消费 channel，批量写入 usage_logs 表

stats/aggregator.go
        │
        ├── 每隔 usage_sync 时间（默认 5 分钟）触发一次
        │
        └── 从 usage_logs 聚合数据，更新 daily_stats 表
            （按 date + user_id + model 维度聚合）
```

---

## 11. 前端架构

### 11.1 技术选型

- React 19 + TypeScript
- Tailwind CSS v4
- Vite（构建工具）
- React Router v7（客户端路由）
- Axios（HTTP 客户端，配置 401 自动跳转登录页拦截器）

### 11.2 页面列表

| 路径 | 页面 | 权限 |
|------|------|------|
| `/login` | 登录页 | 公开 |
| `/` | Dashboard 概览 | 登录用户 |
| `/keys` | API Key 管理 | 登录用户 |
| `/usage` | 用量统计 | 登录用户 |
| `/applications` | 模型申请 | 登录用户 |
| `/admin/users` | 用户管理 | 管理员 |
| `/admin/applications` | 申请审批 | 管理员 |
| `/admin/usage` | 全局用量 | 管理员 |

### 11.3 认证状态管理

`context/AuthContext.tsx` 提供全局认证状态，包含：
- 当前用户信息（itcode、name、role）
- 登录/登出方法
- 路由守卫（未登录自动跳转 `/login`）

### 11.4 Axios 拦截器

```typescript
// api.ts
axios.interceptors.response.use(
  response => response,
  error => {
    if (error.response?.status === 401) {
      window.location.href = '/login'
    }
    return Promise.reject(error)
  }
)
```

---

## 12. 构建与部署

### 12.1 构建脚本（scripts/build.sh）

```bash
#!/bin/bash
set -e

# 1. 构建前端
cd web
npm ci
npm run build
cd ..

# 2. 构建后端（无 CGO 依赖）
CGO_ENABLED=0 go build -o bin/gateway ./cmd/server
```

构建产物：
- `bin/gateway`：单一可执行文件
- `web/dist/`：前端静态资源（由 Go 程序内嵌或静态服务）

### 12.2 运行

```bash
# 使用默认配置路径 config/config.yaml
./bin/gateway

# 指定配置文件路径
CONFIG_PATH=/etc/claude-gateway/config.yaml ./bin/gateway
```

### 12.3 启动行为

1. 加载配置文件（`CONFIG_PATH` 环境变量 > 默认路径）
2. 初始化 Logrus 日志
3. 自动创建 `data/` 目录
4. 初始化 SQLite 数据库，执行 schema 迁移
5. 从数据库加载所有 active API Key 到 KeyStore（内存）
6. 对所有 enabled 后端执行启动验证（GET /v1/models）
7. 启动用量收集器和聚合器 goroutine
8. 启动 Gin HTTP Server

### 12.4 目录结构（运行时）

```
./
├── bin/gateway          # 可执行文件
├── config/config.yaml   # 配置文件
├── data/
│   └── gateway.db       # SQLite 数据库（自动创建）
└── web/dist/            # 前端静态资源
```

---

## 13. 安全设计

| 安全点 | 实现方式 |
|--------|----------|
| Session 防篡改 | Signed Cookie（HMAC，session_secret） |
| 验证码暴力破解防护 | 发送接口限流 10次/分钟 |
| 邀请码注册控制 | send-code 和 login 均需校验 invite_code |
| API Key 安全 | 内存 KeyStore，O(1) 查找，不在日志中打印 |
| 后端 API Key 隔离 | 每个后端独立配置，不暴露给前端 |
| 用户状态控制 | disabled 用户的 Session 和 API Key 均被拒绝 |
| 管理员接口隔离 | `/admin/api/*` 路由独立校验 admin 角色 |

---

## 14. 扩展性说明

- 后端列表支持动态配置，重启生效
- 负载均衡权重可按后端性能差异调整
- `usage_sync` 聚合间隔可根据数据量调整
- SQLite 适合中小规模部署；如需更高并发写入，可替换为 PostgreSQL（需修改 db 层）
- 前端通过 Vite 构建，支持独立部署或由 Go 程序内嵌服务

