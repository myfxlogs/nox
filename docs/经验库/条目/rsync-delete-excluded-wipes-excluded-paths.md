---
schema_version: 2
id: rsync-delete-excluded-wipes-excluded-paths
kind: pitfall
risk: high
status: verified
keywords: [rsync, --delete-excluded, --delete-delay, 生产部署, .env 误删, 宿主工具]
scope: [部署, SG /opt/arb, cron 宿主工具]
summary: rsync 部署时 --delete-excluded 会把「被 --exclude 排除的路径」在接收端物理删除——本想保护 .env/bin/ 反而全灭。生产树 rsync 只可 --delete（且必须审视接收端独有的非仓文件清单），禁 --delete-excluded。
first_seen: 2026-10-08
last_verified: 2026-10-08
skills: []
evidence_refs:
  - type: decision
    ref: D-120
  - type: decision
    ref: D-121
---

## 事实/症状

D-120+D-121 合并部署时，向 SG `/opt/arb` 推送代码用了
`rsync -az --exclude='.env*' --exclude='bin/' --exclude='web/dist' --exclude='web/node_modules' --delete-delay --delete-excluded`。

意图：保护接收端私有文件不被删。实际效果相反——`--delete-excluded` 语义=「排除路径在接收端也执行删除」，SG 的 `.env`（含 ARB_MASTER_KEY/AUTH_TOKEN/PROXY_TOKEN）、`bin/`（xasset-monitor/universe-radar/credread 宿主二进制+*-run.sh 包装，3 个 cron 引用）、`logs/`、`web/dist` 全部被清。

事故窗口内旧容器仍健康运行（env 已加载进进程），未造成停机；但任何 compose up/restart 都会因 .env 缺失把 4 个 `${}` 插值置空。

## 根因或策略解释

`--delete-excluded` 的语义是「excluded 也算 delete 范围」而非「excluded 免于 delete」。排除清单只挡传输，挡不住删除。`--delete-delay` 只改删除时机不改范围。

## 处置

恢复：①`.env` 从仍在运行的 arb-core 容器 `docker inspect .Config.Env` 重建（9 键，chmod 600，compose config 验证通过）；②`bin/` 用仓内 `scripts/build_host_tools.sh` 重建 + `scripts/*-run.sh` 覆写包装（D-116 归一）；③`logs/` mkdir 重建。

规范：SG `/opt/arb` 同步一律 `rsync -az --delete`（无 excluded）+ 事前 `ls` 核对接收端非仓文件清单（.env/bin//logs/docs/报告 等）；或干脆只 rsync 有变动的子路径。

## 证据

- check: `ssh SG 'ls /opt/arb/.env /opt/arb/bin/'` → 部署后双双 No such file
- result: `--delete-excluded` 实证删除接收端排除路径；容器 env 捞回 13 变量重建 .env
- ref: D-120/D-121 部署批次 2026-10-08

## 反证条件

- check: rsync man 页 --delete-excluded 段
- falsifies_when: 无（行为已实证）

## 适用边界

任何「源树 → 含私有文件的部署树」rsync。只读拉取方向不受影响。
