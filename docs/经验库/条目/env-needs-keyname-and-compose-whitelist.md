---
schema_version: 2
id: env-needs-keyname-and-compose-whitelist
kind: pitfall
risk: medium
status: verified
keywords: [env, .env, docker-compose, environment 白名单, 键名猜错, 配置不生效, envFloat]
scope: [ops, config]
summary: .env 写键不生效的两层坑——键名必须对拍 config.go envFloat 定义处，且 docker-compose environment 是白名单（未列出的键进不了容器）
first_seen: 2026-08-21
last_verified: 2026-08-21
skills: []
evidence_refs:
  - type: doc
    ref: internal/config/config.go:99,126
  - type: commit
    ref: 0152bc4 后配置收紧 commit
---

## 事实/症状

收紧检测门槛时凭日志字段名猜 env 键 `ARB_CROSS_CFD_BPS` 写入 .env 并重启——**不生效**（容器内 grep 零命中）。两层原因：① 真实键名是 `ARB_CROSS_MIN_GROSS_BPS_CFD`（日志打印名 cross_cfd_bps ≠ env 键名）；② docker-compose `environment:` 是显式白名单，.env 只是变量替换源，compose 未列出的键根本不进容器。

## 根因或策略解释

config 日志用短字段名打印，与 env 键名无映射关系；compose 显式枚举 environment 是刻意的最小暴露面设计（非 env_file 直通）。跳过对拍直接写 .env = 两层都可能空。

## 处置

改任何 env 前双对拍：① `grep '<envFloat("ARB_...' internal/config/config.go` 拿真实键名与默认值；② `grep <键名> docker-compose.yml` 确认白名单有行，没有则加 `${键:-代码默认值}` 行（默认值单一真相留在 config.go，compose 只透传）。生效验证 = 容器启动 config 日志打印值 + `docker exec <c> env | grep <键>`。

## 证据

- check: `docker exec arb-core env | grep CROSS_CFD`（修正前）→ 0；config.go:99 `envFloat("ARB_CROSS_MIN_GROSS_BPS_CFD", 0.1)`
- result: 修正键名+compose 加行后 cross_cfd_bps=5.00，45s 窗口新机会全部 ≥5bps（此前成片 0.x~3.x）
- ref: 2026-08-21 门槛收紧会话
- check: 2026-09-24 D-425 R3——ARB_INTRA_* 六件（含 kill-switch）只写 .env/代码默认，compose environment（109 个 ARB_*）零同步；`docker exec arb-core env | grep ARB_INTRA` 修正前 0 行
- result: kill 演练被迫走 docker-compose.override.yml 临时注入（服务名须 arb-core 非 core）；compose 六行 `${VAR:-默认}` 补齐后 env 六件在场。附坑：演练注入的 .env 残留值会在补齐后立即生效（ARB_INTRA_KILL=true 残留=轨道会被永久 kill），运维演练后必须清残留
- ref: D-425 R3 返修单（4b0045fe/cdf51734）

## 反证条件

- check: 若 compose 改用 env_file 直通或 config 键名与日志名统一
- falsifies_when: compose 改为 env_file 全量透传 → 白名单坑失效，条目收窄至键名对拍

## 适用边界

docker-compose 显式 environment 模式的项目。helm/k8s ConfigMap 类似机制同理。

**已晋升全局库**：~/kb/pitfalls/2026-08-21-env-keyname-and-compose-whitelist.md（泛化版，2026-08-21）。


## 追加坑（2026-08-28 D-133，同域）

- **行内注释污染值**：`.env` 写 `KEY=0  # 说明` → 值含注释串 → Go envFloat/envDur 解析失败**静默回落默认值**=覆盖无效且无报错。.env 值行禁行内注释，说明写在 compose 注释位或独立 # 行。
- **末行无换行符连体**：`echo "K=V" >> .env` 时原文件末行无 `
` → 新行与旧行拼成一行，两个 KEY 全失效。追加前先确认尾换行（或用脚本重写）。
