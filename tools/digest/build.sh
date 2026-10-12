#!/usr/bin/env bash
# build.sh —— D-512 T7：digest 预编译（cron 由 go run 改二进制直跑，消除每次编译开销）。
# 用法：./build.sh（tools/digest 内产出 digest-bin）；cron 行示例：
#   10 6 * * * cd /opt/nox/tools/digest && ./digest-bin -commit >> digest.log 2>&1
set -euo pipefail
cd "$(dirname "$0")"
go build -o digest-bin .
echo "digest-bin built."
