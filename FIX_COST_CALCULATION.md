# 费用统计一致性修复

## 问题描述

`/api/dashboard` 返回的 `backend_daily_used` 与数据库 `daily_stats` 和 `usage_logs` 统计结果不一致：

- **backend_daily_used (内存实时统计)**: 102.6 USD (只统计 statusCode < 400)
- **daily_stats/usage_logs (数据库)**: 184.88 USD (统计所有请求)
- **差异**: 82.28 USD (失败请求的费用)

### 数据详情

| 状态码 | 请求数 | 费用 (USD) | 说明 |
|--------|--------|-----------|------|
| 200 (成功) | 46 | 102.06 | 成功的请求 |
| 401 (未授权) | 1 | 1.06 | 认证失败 |
| 503 (服务不可用) | 75 | 81.76 | 服务器错误 |
| **总计** | **122** | **184.88** | 所有请求 |

## 根因

代码中存在不一致的计费逻辑：

1. **内存实时统计** (KeyStore.AddDailyCost)：
   - 条件：`if cost > 0 && statusCode < 400`
   - 只统计成功请求

2. **数据库统计** (usage_logs/daily_stats)：
   - 所有请求都记录，包括失败请求
   - 没有 status_code 过滤

## 解决方案

**采用方案B：统一统计所有请求，但失败请求费用为0**

### 修改内容

修改了3个文件中的费用计算逻辑：

#### 1. internal/proxy/handler.go (Backend 代理)

```diff
-	cost := costUSD(model, inputTokens, outputTokens, cacheRead, cacheWrite, pricing)
+	// Calculate cost: only charge for successful requests (statusCode < 400)
+	cost := 0.0
+	if statusCode < 400 {
+		cost = costUSD(model, inputTokens, outputTokens, cacheRead, cacheWrite, pricing)
+	}
...
-	// Accumulate backend daily cost for per-user quota tracking
-	if cost > 0 && statusCode < 400 {
+	// Accumulate backend daily cost for per-user quota tracking (统一统计所有请求)
+	if cost > 0 {
		h.keyStore.AddDailyCost(info.UserID, cost)
	}
```

#### 2. internal/publicproxy/handler.go (Public 代理)

```diff
-	cost := costUSD(provider, model, inputTokens, outputTokens, cacheRead, cacheWrite)
+	// Calculate cost: only charge for successful requests (statusCode < 400)
+	cost := 0.0
+	if statusCode < 400 {
+		cost = costUSD(provider, model, inputTokens, outputTokens, cacheRead, cacheWrite)
+	}
...
-	// Accumulate daily cost for per-user quota tracking
-	if cost > 0 && statusCode < 400 {
+	// Accumulate daily cost for per-user quota tracking (统一统计所有请求)
+	if cost > 0 {
		h.keyStore.AddDailyCost(info.UserID, cost)
	}
```

#### 3. internal/awsproxy/handler.go (AWS Bedrock 代理)

```diff
-	cost := AWSCostUSD(reqModel, inputTokens, outputTokens, cacheRead, cacheWrite, cfg.ModelPricing)
+	// Calculate cost: only charge for successful requests (statusCode < 400)
+	cost := 0.0
+	if statusCode < 400 {
+		cost = AWSCostUSD(reqModel, inputTokens, outputTokens, cacheRead, cacheWrite, cfg.ModelPricing)
+	}
...
-	// Accumulate AWS daily and monthly cost for per-user quota tracking
-	if cost > 0 && statusCode < 400 {
+	// Accumulate AWS daily and monthly cost for per-user quota tracking (统一统计所有请求)
+	if cost > 0 {
		h.keyStore.AddAWSDailyCost(keyInfo.UserID, cost)
		h.keyStore.AddAWSMonthlyCost(keyInfo.UserID, cost)
	}
```

## 效果

修复后的行为：

1. ✅ **失败请求 (statusCode >= 400) 费用为 0**
   - 不计入用户配额
   - 不浪费用户额度

2. ✅ **所有请求都记录到数据库**
   - usage_logs 记录所有请求（包括失败）
   - 失败请求的 cost_usd = 0

3. ✅ **内存统计与数据库统计一致**
   - backend_daily_used = SUM(cost_usd) from usage_logs
   - 两者结果相同

4. ✅ **保留失败请求的 token 消耗数据**
   - input_tokens、output_tokens 仍然记录
   - 方便排查问题和监控

## 验证

编译测试通过：
```bash
go build -o /tmp/gateway-test ./cmd/server
# 编译成功，无错误
```

## 影响范围

- Backend 代理 (internal/proxy)
- Public 代理 (internal/publicproxy)  
- AWS Bedrock 代理 (internal/awsproxy)

## 业务逻辑

**新的计费规则**：
- 成功请求 (2xx, 3xx)：正常计费
- 客户端错误 (4xx)：不计费（如认证失败、参数错误）
- 服务端错误 (5xx)：不计费（如服务不可用、超时）

**理由**：
- 失败请求没有提供有效服务，不应消耗用户配额
- 服务端错误是系统问题，不应由用户承担成本
- 客户端错误通常是配置问题，不应计入正常消耗

## 注意事项

⚠️ **此修复需要重启服务生效**

⚠️ **历史数据不受影响**：
- 已存在的 usage_logs 数据不会被修改
- 新请求从重启后开始按新规则计费

⚠️ **监控建议**：
- 监控 503 等服务端错误的频率
- 如果大量失败请求，需要排查后端稳定性
