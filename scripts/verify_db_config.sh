#!/bin/bash
# SQLite 配置验证脚本

DB_PATH="${1:-data/gateway.db}"

if [ ! -f "$DB_PATH" ]; then
    echo "错误: 数据库文件不存在: $DB_PATH"
    exit 1
fi

echo "=== SQLite 配置验证 ==="
echo "数据库: $DB_PATH"
echo

echo "1. 当前 PRAGMA 配置:"
sqlite3 "$DB_PATH" <<EOF
PRAGMA journal_mode;
PRAGMA synchronous;
PRAGMA cache_size;
PRAGMA wal_autocheckpoint;
PRAGMA mmap_size;
EOF

echo
echo "2. 数据库统计:"
sqlite3 "$DB_PATH" <<EOF
SELECT
    'usage_logs 总记录数' AS metric,
    COUNT(*) AS value
FROM usage_logs
UNION ALL
SELECT
    'usage_logs 最近24小时记录数',
    COUNT(*)
FROM usage_logs
WHERE created_at >= datetime('now', '-1 day')
UNION ALL
SELECT
    'pending_analysis 待处理数',
    COUNT(*)
FROM pending_analysis;
EOF

echo
echo "3. WAL 文件大小:"
ls -lh "${DB_PATH}-wal" 2>/dev/null || echo "WAL 文件不存在或为空"

echo
echo "4. 索引健康检查:"
sqlite3 "$DB_PATH" "PRAGMA integrity_check;" | head -5

echo
echo "=== 预期配置（优化后）==="
echo "journal_mode: wal"
echo "synchronous: 1 (NORMAL)"
echo "cache_size: -65536 (64MB)"
echo "wal_autocheckpoint: 5000"
echo "mmap_size: 268435456 (256MB)"
