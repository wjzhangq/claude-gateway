# Slow Query Logging

SQLite 慢查询日志功能已实现，可以帮助识别性能瓶颈。

## 配置

在 `config/config.yaml` 中配置慢查询阈值：

```yaml
database:
  path: data/gateway.db
  slow_query_threshold_ms: 1000  # 超过1秒的查询会被记录
```

## 日志格式

慢查询日志保存在 `log/slow-query-YYYY-MM-DD.log`，每天自动轮转。

日志格式（JSON）：
```json
{
  "caller": "user.go:125",
  "duration_ms": 1523,
  "level": "warning",
  "msg": "slow query",
  "sql": "SELECT * FROM users WHERE created_at > ?",
  "time": "2026-09-22T15:51:20.283+08:00"
}
```

字段说明：
- `duration_ms`: 查询执行时间（毫秒）
- `sql`: 执行的 SQL 语句
- `caller`: 代码位置（文件:行号）
- `time`: 查询完成时间

## 监控和分析

### 实时监控
```bash
# 实时查看慢查询
tail -f log/slow-query-$(date +%Y-%m-%d).log

# 过滤超过 5 秒的查询
tail -f log/slow-query-*.log | jq 'select(.duration_ms > 5000)'
```

### 分析历史数据
```bash
# 统计最慢的 10 个查询类型
cat log/slow-query-*.log | jq -r '.sql' | sort | uniq -c | sort -rn | head -10

# 查看特定表的慢查询
cat log/slow-query-*.log | jq 'select(.sql | contains("usage_logs"))'

# 按代码位置分组统计
cat log/slow-query-*.log | jq -r '.caller' | sort | uniq -c | sort -rn
```

## 优化建议

如果发现慢查询：

1. **添加索引** - 检查 WHERE/JOIN 条件是否有索引支持
2. **优化查询** - 避免 SELECT *，减少返回列数
3. **分页查询** - 大结果集使用 LIMIT/OFFSET 分页
4. **批量操作** - 多次单条插入改为批量事务
5. **缓存结果** - 频繁查询的数据考虑缓存

## 性能影响

慢查询日志功能性能开销极小：
- 所有查询都计时（纳秒级操作）
- 仅超过阈值的查询写日志
- 使用异步日志写入，不阻塞数据库操作

## 测试

运行测试验证功能：
```bash
go test ./internal/db -run TestSlowQueryLogging -v
```

## 实现细节

- 包装了 `*sql.DB` 的所有查询方法（Query, Exec, QueryRow 及其 Context 变体）
- 使用 `runtime.Caller(1)` 捕获调用位置
- 使用 `time.Now()` / `time.Since()` 计时
- 通过 `logger.LogSlowQuery()` 记录到日志
- 日志文件每天自动轮转
