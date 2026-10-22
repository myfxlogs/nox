---
schema_version: 2
id: deploy-sync-stale-files
kind: pitfall
risk: high
status: verified
keywords: [deploy_sync, rsync, --delete, 陈件, stale file, go build, 已删文件残留, 部署树, SG]
scope: [deployment, tooling]
summary: deploy_sync.sh 用 rsync --files-from=git ls-files 只推不清——git 已删文件永留 SG /opt/nox，Go 按目录编译把陈件吃回去（删了的 ticker.go 引已删字段→生产 build 炸）；删文件类提交部署前须按清单 prune SG 陈件
first_seen: 2026-10-22
last_verified: 2026-10-22
skills: []
---

# deploy_sync 无 --delete：SG 部署树陈件进编译面

## 病灶（D-663 部署实证，2026-10-22）

merge @e34f9e3b0 本地 `go build` 绿，SG 侧 `go build` 炸：
`binance_market_ws_ticker.go: tickerEmit undefined`。
该文件 git 已删（D-663 退役），但 `scripts/deploy_sync.sh` 用
`rsync --files-from=<(git ls-files)`——只推跟踪面**不删陈件**，
SG `/opt/nox` 累积全部历史已删文件。`go build` 按目录编译，
陈件照样进包。

## 实测面

`git ls-files '*.go'` vs SG `find . -name '*.go'` 差集=11 件陈件
（1 件破编译 + 4 件旧 _test + 6 件 tools/ 陈件）——本次首炸而已，
存量早积。

## 规则

- 部署前校验：SG 侧 `find -name '*.go'` 对 `git ls-files` 差集应为空；
  非空→按清单 `rm`（已实证安全操作）。
- **不可**给 rsync 加裸 `--delete`：--files-from 下会扫删 SG 侧
  .env/bin/logs/web/dist 等非跟踪必需件。清陈件走「差集清单定点删」。
- 改 deploy_sync 的正解=部署步尾加 `.go` 差集 prune 步
  （本条目为坑档；修件走 D-665 或独立工具票）。
- 同律适用任何「按目录/通配编译」的语言面（go 最典型，
  python/JS 的 import 面不吃目录故不病）。
