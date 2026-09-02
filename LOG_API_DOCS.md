# 日志查询 API 文档

## 1. 最近错误日志查询

**接口路径：** `GET /admin/api/db/errors/recent`

**功能：** 查询 `backend.log` 和 `dasheng.log` 中状态码非 200 的错误记录

**查询参数：**
- `limit` - 返回条数，默认 100，最大 1000
- `hours` - 最近 N 小时内，默认 24
- `status_code` - 过滤特定状态码（如 429, 500）
- `user_id` - 过滤特定用户 ID
- `channel` - 过滤通道：`backend` / `aws`（不传则两者都查）

**示例请求：**
```bash
# 查询最近 24 小时所有错误（默认）
curl "http://localhost:8080/admin/api/db/errors/recent"

# 查询最近 1 小时的 429 错误
curl "http://localhost:8080/admin/api/db/errors/recent?hours=1&status_code=429"

# 查询特定用户的错误，限制返回 50 条
curl "http://localhost:8080/admin/api/db/errors/recent?user_id=10&limit=50"

# 只查询 backend 通道的错误
curl "http://localhost:8080/admin/api/db/errors/recent?channel=backend"
```

**返回示例：**
```json
{
  "errors": [
    {
      "level": "error",
      "status_code": 429,
      "user_id": 10,
      "itcode": "user001",
      "model": "claude-3-5-sonnet",
      "backend": "backend1",
      "time": "2026-09-02T14:23:15.123Z",
      "latency_ms": 120,
      "message": "rate limit exceeded"
    }
  ],
  "total": 150,
  "sources": {
    "backend.log": 100,
    "dasheng.log": 50
  }
}
```

## 2. 最近访问日志查询

**接口路径：** `GET /admin/api/db/logs/recent`

**功能：** 查询所有访问记录（包括成功和失败），从 `backend.log` 和 `dasheng.log` 读取

**查询参数：**
- `limit` - 返回条数，默认 100，最大 1000
- `hours` - 最近 N 小时内，默认 1
- `user_id` - 过滤特定用户 ID
- `status_code` - 过滤特定状态码
- `model` - 过滤特定模型名称
- `channel` - 过滤通道：`backend` / `aws`（不传则两者都查）

**示例请求：**
```bash
# 查询最近 1 小时所有访问（默认）
curl "http://localhost:8080/admin/api/db/logs/recent"

# 查询最近 12 小时特定模型的访问
curl "http://localhost:8080/admin/api/db/logs/recent?hours=12&model=claude-3-5-sonnet"

# 查询成功的请求（status_code=200）
curl "http://localhost:8080/admin/api/db/logs/recent?status_code=200&limit=200"

# 只查询 AWS 通道的日志
curl "http://localhost:8080/admin/api/db/logs/recent?channel=aws"
```

**返回示例：**
```json
{
  "logs": [
    {
      "level": "info",
      "status_code": 200,
      "user_id": 10,
      "itcode": "user001",
      "model": "claude-3-5-sonnet",
      "input_tokens": 1000,
      "output_tokens": 500,
      "cost_usd": 0.0123,
      "time": "2026-09-02T14:25:30.456Z",
      "latency_ms": 850,
      "backend": "backend1"
    }
  ],
  "total": 245,
  "sources": {
    "backend.log": 200,
    "dasheng.log": 45
  }
}
```

## 实现说明

### 日志文件路径
- `backend.log` - 位于配置文件指定的 `log.file` 路径
- `dasheng.log` - 位于配置文件指定的 `log.dasheng_file` 路径

### 日志格式
两个日志文件均为 JSON Lines 格式（每行一条 JSON 记录），包含字段：
- `time` - RFC3339 格式时间戳
- `level` - 日志级别（info/error）
- `status_code` - HTTP 状态码
- `user_id` - 用户 ID
- `itcode` - 用户工号
- `model` - 模型名称
- `input_tokens` / `output_tokens` - token 数量
- `cost_usd` - 成本（美元）
- `latency_ms` - 延迟（毫秒）
- `backend` - 后端名称

### 查询逻辑
1. 从文件末尾反向读取最近的日志行
2. 合并两个文件的结果，按时间降序排序
3. 应用过滤条件（时间范围、状态码、用户等）
4. 返回符合条件的前 N 条记录

### 性能考虑
- 采用反向读取，避免加载整个文件
- 单次读取最多 1000 条，防止内存溢出
- 日志文件按时间追加，新记录在文件末尾
