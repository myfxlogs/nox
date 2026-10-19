#!/usr/bin/env bash
# D-116：宿主侧定时工具重建——cron 裸奔的 go 二进制（xasset-monitor/universe-radar/credread）
# 独立于 nox-core 镜像管线，不重建=漂移（9-30 构建落后 c00505e35 白名单修补实证）。
# 用法：./scripts/build_host_tools.sh（repo 根目录跑；需宿主 go toolchain + git）
set -euo pipefail
cd "$(dirname "$0")/.."
OUT="${NOX_BIN_DIR:-./bin}"
mkdir -p "$OUT"
SHA="$(git rev-parse --short HEAD 2>/dev/null || echo nogit)"
for t in tools/xasset-monitor tools/universe-radar tools/credread tools/state-audit; do
  n="$(basename "$t")"
  go build -ldflags "-s -w -X main.buildSHA=$SHA" -o "$OUT/$n" "./$t"
  echo "built $OUT/$n @$SHA"
done
