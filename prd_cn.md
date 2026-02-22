# Claude Code Gateway 开发文档

## 1. 引言

### 1.1 项目概述

Claude Code Gateway 是一个为企业内部提供统一、可控的 Claude 模型访问入口的平台。它实现了 Anthropic 官方风格和 OpenAI 风格的 API 兼容，并提供完整的 Web 管理后台，支持用户管理、API Key 管理、使用统计、费用控制和模型使用审批等功能。

### 1.2 项目目标

- **统一接口**：对接多个后端 AI 服务（如 Anthropic、OpenAI），提供统一的访问入口。
- **安全可控**：通过 API Key 鉴权、用户配额、审批流程，确保服务的安全性和成本可控。
- **可观测性**：记录详细的使用日志，支持按模型、用户、时间统计 Token 消耗和费用。
- **高可用**：支持多后端自动切换（带权重和健康检查），提高服务稳定性。
- **高性能**：代理转发路径必须高效，认证信息全内存加载，后端连接池复用，统计异步处理，避免影响主流程。

### 1.3 适用范围

本文档面向参与 Claude Code Gateway 开发、维护的工程师，涵盖技术架构、模块设计、数据库设计、接口规范、配置说明和部署指南。

## 2. 技术栈

| 组件     | 技术选件             | 说明                                                         |
| :------- | :------------------- | :----------------------------------------------------------- |
| 后端语言 | Go 1.25+             | 高性能、并发支持好                                           |
| Web 框架 | Gin                  | 轻量级、路由性能高                                           |
| 数据库   | SQLite               | 默认存储，便于部署；驱动使用 [modernc.org/sqlite](https://modernc.org/sqlite) |
| 前端     | React + Tailwind CSS | 无复杂框架，降低维护成本                                     |
| 会话管理 | Gin Session + Cookie | 基于 Cookie 存储 Session ID                                  |
| 配置管理 | YAML                 | 使用 config.yaml 配置文件                                    |
| 日志     | Logrus               | 结构化日志，记录请求、错误等，日志打印使用用户 itcode        |

## 3. 系统架构

### 3.1 整体架构

text

```
+----------------+      +-----------------+      +-----------------+
|  客户端 (SDK)  | ---> |  Claude Code Gateway  | ---> |   后端 AI API   |
| (OpenAI/自定义)| <--- |   代理服务       | <--- | (Anthropic 等)  |
+----------------+      +-----------------+      +-----------------+
                            |         ^
                            v         |
                        +-----------------+
                        |    SQLite       |
                        +-----------------+
```



### 3.2 模块划分

- **配置模块**：加载 config.yaml，提供全局配置访问。
- **数据库模块**：使用 [modernc.org/sqlite](https://modernc.org/sqlite) 操作 SQLite。
- **中间件**：包含认证中间件、配额检查中间件、日志中间件。
- **认证模块**：验证码登录、Session 管理、API Key 验证。**所有 API Key 全量加载到内存**，实现 O(1) 级别验证，避免查库。
- **用户管理模块**：用户 CRUD、状态管理、配额设置。
- **API Key 管理模块**：Key 的生成、存储、验证、过期管理。
- **代理转发模块**：接收请求，根据配置选择后端，**使用连接池复用连接**，转发并返回响应。
- **统计模块**：记录每次请求的 Token 使用量，**异步处理**，通过内存队列或 channel 解耦，避免阻塞转发流程。
- **审批模块**：处理模型使用申请和审批流程。
- **管理后台前端**：提供 Web 界面供管理员和用户操作，**不考虑效率，仅面向少量管理用户**。

### 3.3 数据流

#### 代理请求流程（高性能路径）

1. 客户端携带 API Key 调用代理接口（如 `/v1/chat/completions`）。
2. **AuthMiddleware** 直接从内存 Map 中查找 Key 对应的用户信息和权限，若不存在或已禁用则快速返回 401。
3. **QuotaMiddleware** 从内存中的用户配额缓存检查是否超限（配额可定期同步或实时更新）。
4. 代理模块从**连接池**中获取一个到后端（如 Anthropic）的持久连接，根据负载均衡策略选择后端，替换认证头后转发请求。
5. 收到后端响应后，**统计模块通过 channel 异步记录使用量**，不等待写入完成。
6. 直接返回响应给客户端（若为流式响应，则边接收边转发，同时异步统计）。

#### 管理后台流程（低性能要求）

1. 浏览器访问 `/admin/`，用户输入 itcode 获取验证码，登录。
2. 登录成功后，Session 建立，后续请求携带 Cookie。
3. 用户可在后台管理自己的 API Key，查看使用统计。
4. 管理员可访问用户管理、审批、后端 endpoint 统计等页面。

## 4. 模块设计

### 4.1 配置模块

- **位置**：`config/config.go`
- **功能**：读取 `config.yaml`，解析为结构体，提供全局单例。
- **关键结构体**：

go

```
type Config struct {
    Port          int          `mapstructure:"port"`            // 服务端口
    DBPath        string       `mapstructure:"db"`              // sqlite数据库地址
    DefaultAdmin  string       `mapstructure:"default_admin"`   // 初始化默认管理员 itcode
    SendCodeURL   string       `mapstructure:"send_code_url"`   // 登录发送验证码api
    InviteCode    string       `mapstructure:"invite_code"`     // 邀请码，发送验证码时填写
    UsageSyncTime int          `mapstructure:"usage_sync_time"` // 内存使用统计数据同步到数据库的间隔（秒）
    BackendAPIs   []BackendAPI `mapstructure:"backend_apis"`
    MaxIdleConns  int          `mapstructure:"max_idle_conns"`  // 连接池最大空闲连接数
}

type BackendAPI struct {
    URL    string `mapstructure:"url"`
    Key    string `mapstructure:"key"`
    Weight int    `mapstructure:"weight"` // 权重，用于负载均衡
    Type   string `mapstructure:"type"`   // 可选，标识后端类型（anthropic/openai）
}
```



### 4.2 数据库模块

- **位置**：`models/` 目录下。
- **驱动**：`modernc.org/sqlite`
- **初始化**：在 `main.go` 中连接数据库并自动迁移表结构。
- **注意**：代理请求路径**不允许**同步查询数据库，所有关键数据（API Key、用户状态、配额）必须常驻内存。

### 4.3 中间件

- **AuthMiddleware**：从请求头提取 `Authorization: Bearer {token}`，在内存中的 `map[string]*APIKeyInfo` 中查找 Key，验证有效性，将用户信息存入上下文（如 `*gin.Context` 的 `Set`）。内存 Map 需支持并发读写，使用 `sync.RWMutex` 或 `sync.Map`。
- **SessionAuthMiddleware**：从 Cookie 解析 Session，验证用户登录状态，用于管理后台接口。此路径可以查库或查 Session 存储。
- **QuotaMiddleware**：在代理请求前检查用户配额（Token 或费用），若超限则拒绝。配额信息也应在内存中缓存，定期从数据库刷新或通过事件更新。
- **LoggerMiddleware**：记录请求方法、路径、状态码、耗时。

### 4.4 认证模块

#### 4.4.1 验证码登录（管理后台）

- **发送验证码**：`POST /admin/api/auth/send_code`，调用外部发送验证码服务（配置 `send_code_url`），将验证码与 itcode 在内存中临时存储，过期时间5分钟。
- **登录**：`POST /admin/api/auth/login`，验证验证码，成功则创建 Session，返回用户信息。
- **登出**：`POST /admin/api/auth/logout`，销毁 Session。

#### 4.4.2 API Key 验证（高性能路径）

- **API Key 生成**：随机32字节字符串，前缀固定为 `sk-`。

- **存储**：数据库中存储 Key 明文（或加密），同时维护一个内存中的只读副本。启动时全量加载所有有效 Key，之后通过监听数据库变更（或定时全量刷新）保持同步。

- **内存结构**：

  go

  ```
  type APIKeyInfo struct {
      Key       string
      Itcode    string
      Status    int
      ExpireAt  *time.Time
      Quota     *Quota // 可关联用户配额
  }
  var keyMap sync.Map // key -> *APIKeyInfo
  ```

  

- **验证**：`AuthMiddleware` 直接从 `keyMap` 读取，若不存在或已过期/禁用则返回 401。时间复杂度 O(1)，无数据库查询。

### 4.5 用户管理模块

- **用户模型字段**：
  - `ID` (uint, 主键)
  - `Itcode` (string, 唯一索引)
  - `Status` (int, 0=禁用, 1=未审核, 2=启用)
  - `Quota` (JSON，如 `{"monthly_tokens": 1000000, "monthly_cost": 100}`)
  - `CreatedAt` (time.Time)
  - `UpdatedAt` (time.Time)
- **默认管理员**：首次启动时，若数据库为空，则根据 `default_admin` 创建管理员用户。
- 用户配额信息也需加载到内存，与 API Key 关联，便于快速配额检查。

### 4.6 API Key 管理模块

- **API Key 模型字段**：
  - `ID` (uint)
  - `Key` (string) // 实际 Key
  - `Itcode` (string) // 外键关联用户
  - `Name` (string) // 备注名称
  - `Status` (int) // 0=禁用 1=启用
  - `ExpireAt` (*time.Time) // 过期时间，为空表示永不过期
  - `CreatedAt` (time.Time)
  - `LastUsedAt` (*time.Time) // 最后使用时间（可选）
- **接口**：提供创建、列表、禁用、启用、删除等功能。当 Key 状态变更时，需同步更新内存中的 `keyMap`。

### 4.7 代理转发模块

#### 4.7.1 后端选择策略

- 支持配置多个后端，每个后端有 URL、API Key 和权重。
- **策略**：默认按权重随机选择；同时记录每个后端的连续错误次数，当错误率过高时暂时剔除，降低其被选中的概率（健康检查/错误计数机制）。

#### 4.7.2 连接池管理

- 为每个后端建立独立的 HTTP 连接池，使用 `http.Transport` 并配置 `MaxIdleConnsPerHost` 和 `MaxIdleConns`，避免频繁建立 TCP 连接。
- 可在配置中指定 `max_idle_conns`，控制全局空闲连接数。
- 连接池由全局的 HTTP 客户端持有，转发时复用。

#### 4.7.3 请求转发

- 转发请求时，保持原始请求的 body 和 headers（除认证头外，需替换为后端自己的 Key）。
- 支持流式响应（SSE），直接透传。边读取后端响应边写入客户端，同时异步统计 Token 使用量（需解析响应体或依赖后端返回的 usage 信息）。

### 4.8 统计模块

- **异步记录**：在代理请求处理完成后（或流式结束时），将使用信息（用户、模型、token 计数、费用等）发送到一个带缓冲的 channel，由独立的 worker goroutine 批量写入数据库。
- **避免阻塞**：channel 满时可降级（如丢弃或使用超时），确保转发路径不受影响。
- **聚合统计**：使用 `usage_sync_time` 配置的间隔，定期从原始日志表聚合数据到 `daily_stats` 汇总表，便于管理后台查询。
- **UsageLog 结构**：
  - `ID` (uint)
  - `Itcode` (string)
  - `APIKeyID` (uint)
  - `Model` (string)
  - `InputTokens` (int)
  - `OutputTokens` (int)
  - `Cost` (float) // 根据模型单价计算
  - `CreatedAt` (time.Time)

### 4.9 审批模块

- **Application 模型**：
  - `ID` (uint)
  - `UserID` (itcode)
  - `Reason` (string)
  - `Status` (string) // pending, approved, rejected
  - `ReviewerID` (uint, 可为空)
  - `ReviewedAt` (*time.Time)
  - `CreatedAt` (time.Time)
- 用户提交申请后，管理员可在后台查看并审批。审批通过后，在用户权限中允许使用该模型（模型权限可放入内存用户信息中）。

### 4.10 管理后台前端

- **位置**：`web/` 目录下。
- **页面包括**：
  - 登录页
  - 仪表盘（使用概览）
  - API Key 管理页
  - 使用统计页（图表）
  - 申请页（用户）
  - 用户管理页（管理员）
  - 审批页（管理员）
  - 后端 endpoint 使用统计页面（管理员）
- **前后端交互**：通过 Fetch API 调用后台接口，渲染数据。此部分不考虑性能优化，仅需功能正确。

## 5. 数据库设计

### 5.1 ER 图

text

```
+----------------+       +----------------+       +----------------+
|     users      |       |    api_keys    |       |  usage_logs    |
+----------------+       +----------------+       +----------------+
| id             |<----->| itcode         |       | itcode         |
| itcode         |       | id             |       | api_key_id     |
| status         |       | key            |       | model          |
| quota          |       | name           |       | input_tokens   |
| created_at     |       | status         |       | output_tokens  |
| updated_at     |       | expire_at      |       | cost           |
+----------------+       | created_at     |       | created_at     |
                         | last_used_at   |       +----------------+
                         +----------------+               |
                                                           |
+----------------+       +----------------+                |
| applications   |       |  daily_stats   |<---------------+
+----------------+       +----------------+
| id             |       | id             |
| user_id        |       | date           |
| reason         |       | itcode         |
| status         |       | model          |
| reviewer_id    |       | total_tokens   |
| reviewed_at    |       | total_cost     |
| created_at     |       | request_count  |
+----------------+       +----------------+
```



### 5.2 表结构说明

- **users**：存储用户基本信息及配额。
- **api_keys**：存储用户创建的 API Key，`key` 字段为明文存储（或加密），但内存中全量加载。
- **usage_logs**：每次请求的详细记录，用于原始数据查询和聚合。此表会快速增长，需考虑定期归档或分区。
- **daily_stats**：按天、用户、模型聚合的统计信息，由定时任务生成，用于管理后台快速展示。
- **applications**：模型使用申请记录。

## 6. API 设计

所有管理后台接口均以 `/admin/api` 为前缀，需要携带登录 Session（Cookie）。

### 6.1 认证接口

#### 发送验证码

- **URL**：`POST /admin/api/auth/send_code`
- **请求体**：

json

```
{
  "invite_code": "xxxxx",
  "itcode": "user123"
}
```



- **响应**：200 OK

json

```
{
  "message": "验证码已发送"
}
```



- **说明**：调用外部接口发送验证码，验证码存储于内存，有效期5分钟。

#### 登录

- **URL**：`POST /admin/api/auth/login`
- **请求体**：

json

```
{
  "itcode": "user123",
  "code": "123456"
}
```



- **响应**：

json

```
{
  "user": {
    "id": 1,
    "itcode": "user123",
    "status": 1,
    "quota": {}
  }
}
```



- **说明**：验证成功后设置 Cookie 会话。

#### 登出

- **URL**：`POST /admin/api/auth/logout`
- **响应**：200 OK

### 6.2 API Key 管理接口

#### 获取当前用户的所有 Key

- **URL**：`GET /admin/api/api_keys`
- **响应**：

json

```
{
  "keys": [
    {
      "id": 1,
      "name": "my-app",
      "key": "sk-...abc",  // 返回完整 Key
      "status": 1,
      "expire_at": "2025-12-31T23:59:59Z",
      "created_at": "..."
    }
  ]
}
```



#### 创建新 Key

- **URL**：`POST /admin/api/api_keys`
- **请求体**：

json

```
{
  "name": "my-app",
  "expire_at": "2025-12-31T23:59:59Z" // 可选
}
```



- **响应**：

json

```
{
  "id": 1,
  "key": "sk-xxxxxxxxxxxx" // 仅创建时返回完整 Key
}
```



#### 禁用/启用 Key

- **URL**：`POST /admin/api/api_keys/:id/disable` 或 `/enable`
- **响应**：200 OK

#### 删除 Key

- **URL**：`DELETE /admin/api/api_keys/:id`
- **响应**：200 OK

### 6.3 使用统计接口

#### 获取统计

- **URL**：`GET /admin/api/usage`
- **查询参数**：
  - `start_date` (可选): YYYY-MM-DD
  - `end_date` (可选): YYYY-MM-DD
  - `user_id` (可选，管理员可用)
  - `model` (可选)
- **响应**：

json

```
{
  "items": [
    {
      "date": "2025-03-21",
      "model": "claude-3-sonnet",
      "input_tokens": 1000,
      "output_tokens": 500,
      "cost": 0.015,
      "requests": 10
    }
  ],
  "total": {
    "input_tokens": 1000,
    "output_tokens": 500,
    "cost": 0.015
  }
}
```



### 6.4 申请审批接口

#### 提交使用申请

- **URL**：`POST /admin/api/apply`
- **请求体**：

json

```
{
  "reason": "需要处理复杂推理任务"
}
```



- **响应**：

json

```
{
  "id": 1,
  "status": "pending"
}
```



#### 审批申请（管理员）

- **URL**：`POST /admin/api/applications/:id/approve` 或 `/reject`
- **请求体**（可选）：

json

```
{
  "reason": "同意"
}
```



- **响应**：200 OK

### 6.5 代理接口（高性能路径）

这些接口不需要 Session，只需要 API Key 认证（通过 `Authorization: Bearer` 头），认证过程全内存，无数据库查询。

#### OpenAI 风格 Chat Completions

- **URL**：`POST /v1/chat/completions`
- **Headers**：

text

```
Authorization: Bearer sk-xxx
Content-Type: application/json
```



- **请求体**（示例）：

json

```
{
  "model": "claude-3-sonnet",
  "messages": [{"role": "user", "content": "Hello"}],
  "stream": false
}
```



- **响应**：与 OpenAI 官方格式一致。

#### Anthropic 风格 Messages

- **URL**：`POST /v1/messages`
- **Headers**：

text

```
Authorization: Bearer sk-xxx
Content-Type: application/json
```



- **请求体**（示例）：

json

```
{
  "model": "claude-3-sonnet",
  "messages": [{"role": "user", "content": "Hello"}],
  "system": "你是一个助手"
}
```



- **响应**：与 Anthropic API 格式一致。

#### 模型列表

- **URL**：`GET /v1/models`
- **Headers**：需 API Key
- **响应**：

json

```
{
  "data": [
    {
      "id": "claude-3-sonnet",
      "object": "model",
      "created": 1677610602,
      "owned_by": "anthropic"
    }
  ]
}
```



- **说明**：可返回静态配置的模型列表，或从后端动态获取（需缓存，避免频繁请求后端）。

## 7. 配置说明

`config.yaml` 示例及字段详解：

yaml

```
# 服务端口
port: 8080

# 数据库文件路径（SQLite）
db: "./data/database.db"

# 默认管理员 itcode（首次启动自动创建）
default_admin: "admin"

# 验证码发送接口地址（POST 请求，接收 JSON {"itcode": "xxx"}）
send_code_url: "https://example.com/api/send_code"

# 管理员邀请码（可选，用于注册时校验）
invite_code: "secret123"

# 使用统计同步周期（秒），用于聚合 daily_stats
usage_sync_time: 3600

# HTTP 连接池最大空闲连接数（每个 host）
max_idle_conns: 100

# 后端 API 配置列表
backend_apis:
  - url: "https://api.anthropic.com/v1"
    key: "sk-ant-xxx"
    weight: 60            # 权重，用于负载均衡
    type: "anthropic"     # 可选，标识后端类型
  - url: "https://api.openai.com/v1"
    key: "sk-openai-xxx"
    weight: 40
    type: "openai"
```



## 8. 项目结构

text

```
.
├── cmd/
│   └── server/              # 主程序入口
│       └── main.go
├── config/                   # 配置加载
│   └── config.go
├── internal/                 # 内部包
│   ├── middleware/           # 中间件
│   │   ├── auth.go
│   │   ├── quota.go
│   │   └── logger.go
│   ├── auth/                 # 认证模块
│   │   ├── session.go
│   │   └── apikey.go         # 内存 Key 管理
│   ├── user/                 # 用户管理
│   │   └── service.go
│   ├── apikey/               # API Key 管理
│   │   └── service.go
│   ├── proxy/                # 代理转发模块
│   │   ├── router.go
│   │   ├── balancer.go       # 负载均衡
│   │   └── client.go         # HTTP 客户端（带连接池）
│   ├── stats/                # 统计模块
│   │   ├── collector.go      # 异步收集（channel + worker）
│   │   └── aggregator.go     # 定时聚合
│   ├── approval/             # 审批模块
│   │   └── service.go
│   └── models/               # 数据模型与数据库操作
│       ├── user.go
│       ├── apikey.go
│       ├── usage.go
│       ├── application.go
│       └── db.go             # 数据库初始化
├── web/                      # 前端项目（React）
│   ├── public/
│   ├── src/
│   │   ├── components/
│   │   ├── pages/
│   │   ├── api/              # 调用后端接口的工具
│   │   └── App.tsx
│   ├── package.json
│   └── ...
├── scripts/                  # 辅助脚本（如数据库迁移）
├── config.yaml               # 配置文件
├── go.mod
├── go.sum
└── README.md
```



## 9. 开发指南

### 9.1 环境要求

- Go 1.25+
- Node.js 18+（用于前端构建）

### 9.2 本地运行

1. 克隆代码库。

2. 修改 `config.yaml` 配置。

3. 运行后端：

   bash

   ```
   go run cmd/server/main.go
   ```

   

4. 进入 `web` 目录，安装依赖并启动前端开发服务器：

   bash

   ```
   cd web
   npm install
   npm start
   ```

   

5. 访问 `http://localhost:3000` 使用管理后台。

### 9.3 测试

- **单元测试**：每个包编写 `*_test.go` 文件。

- **集成测试**：使用 `net/http/httptest` 模拟请求。

- 运行所有测试：

  bash

  ```
  go test ./...
  ```

  

### 9.4 构建

- 后端构建：

  bash

  ```
  go build -o claude-gateway cmd/server/main.go
  ```

  

- 前端构建：

  bash

  ```
  cd web
  npm run build
  ```

  

  构建后的静态文件可嵌入 Go 二进制或单独部署。

## 10. 附录

### 10.1 错误码

| HTTP 状态码 | 错误码            | 说明                  |
| :---------- | :---------------- | :-------------------- |
| 400         | invalid_request   | 请求参数错误          |
| 401         | unauthorized      | 未认证或 API Key 无效 |
| 403         | quota_exceeded    | 配额超限              |
| 403         | model_not_allowed | 模型未审批            |
| 404         | not_found         | 资源不存在            |
| 500         | internal_error    | 服务器内部错误        |

### 10.2 性能优化要点

- **API Key 全内存加载**：启动时加载所有有效 Key，后续通过事件或定时任务同步更新。
- **连接池复用**：每个后端配置独立的 HTTP 连接池，避免频繁 TCP 握手。
- **异步统计**：使用 channel 和 worker 将统计写入与请求处理解耦，确保转发路径无阻塞。
- **配额缓存**：用户配额信息也加载到内存，与 Key 关联，配额检查快速。
- **无数据库查询**：代理请求路径完全避免数据库操作。

|      |      |      |
| :--- | :--- | :--- |
|      |      |      |
|      |      |      |
|      |      |      |
|      |      |      |
|      |      |      |
|      |      |      |