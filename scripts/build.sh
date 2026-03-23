#!/usr/bin/env bash
# build.sh - 构建 Claude Gateway（前端 + 后端）
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

# 解析参数
TARGET_OS="${1:-}"
TARGET_OS=linux
if [[ "$TARGET_OS" == "linux" ]]; then
    export GOOS=linux
    export GOARCH=amd64
    SUFFIX="-linux-amd64"
    echo "==> 目标平台: Linux amd64"
else
    SUFFIX=""
    echo "==> 目标平台: 本机"
fi

echo "==> 构建前端..."
cd web
npm ci
npm run build
cd "$ROOT"

echo "==> 构建后端..."
mkdir -p bin
CGO_ENABLED=0 go build -ldflags="-s -w" -o "bin/gateway${SUFFIX}" ./cmd/server
CGO_ENABLED=0 go build -ldflags="-s -w" -o "bin/sync${SUFFIX}" ./cmd/sync

echo "==> 构建完成：bin/gateway${SUFFIX}  bin/sync${SUFFIX}"
echo "    运行方式：./bin/gateway${SUFFIX} -config config/config.yaml"
echo "    同步方式：./bin/sync${SUFFIX} --fromdb ./database.db --todb ./data/gateway.db"
