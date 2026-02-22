#!/usr/bin/env bash
# build.sh - 构建 Claude Gateway（前端 + 后端）
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

echo "==> 构建前端..."
cd web
npm ci
npm run build
cd "$ROOT"

echo "==> 构建后端..."
mkdir -p bin
CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/gateway ./cmd/server

echo "==> 构建完成：bin/gateway"
echo "    运行方式：./bin/gateway -config config/config.yaml"
