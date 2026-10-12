---
schema_version: 2
id: fossil-docs-feed-code-fossils
kind: pitfall
risk: high
status: verified
keywords: [化石文档, 死端点, 硬编码, fossil-lint, 文档同步, cex-proxy, config.json, 路由漂移, 施工误导, 游离脚本]
scope: [运维文档, 部署拓扑, 配置清单, cron脚本, 全仓]
summary: 文档化石不是文字问题是信息源——README 死端点被施工者照抄成代码硬编码（D-538 两游离脚本实证断流 15h/37h）；拓扑换代后必须同步三区（活文档/仓内清单/部署外带项），并由 fossil-lint 门禁拦截字面回归
first_seen: 2026-09-28
last_verified: 2026-09-28
skills: [事实核验, KB维护]
evidence_refs:
  - type: server_fact
    desc: "bin/xasset-monitor-run.sh 与 bin/universe-radar-run.sh 均硬编码 export BINANCE_PROXY_URL=死IP:8443 覆盖 .env 正确值——fin_xasset_monitor 断更 15h+、fin_cex_radar 断更 37h+（2026-09-28 实测表 max(ts)）"
  - type: server_fact
    desc: "带 token 探针 https://cex.myfxlogs.org/okxws → 400（路由实存生产代理机但仓内 config.json 缺——外带未收编）；/gatespotws → 404（prod 实无此路由，gate spot WS 靠直连兜底活）"
  - type: commit
    desc: "清剿三批 ff4b0e59/691544b2/889ad45c：活文档横幅+冷备重写+config_load 死默认纠官方端点+迁移0156+check_fossil.sh 三门门禁"
  - type: doc
    desc: "practices.md「文档同步纪律」节=纪律 SSOT；scripts/fossil-signatures.conf=签名注册表"
related:
  - cex-proxy-sni-token-routing
  - server-facts-over-docs
---

## 症状

- 施工席按仓内 README/部署文档里的端点施工 → 产出含化石字面的硬编码（脚本 export、代码默认值、SQL 种子），运行链路静默断流。
- 生产止血时给代理机外带加路由 → 仓内清单（config.json/defaultConfig/README）不补登 → 下次重部署断流（/gatespotws、/okxws 两例实证）。
- 同一死端点字面在多介质独立存活：README、部署指南、冷备模板、代码默认值、SQL 种子、cron wrapper——改一处漏五处。

## 根因

拓扑换代（裸IP→CF域名→gate被封改直连）只改了「活的配置面」（.env/DB/生产代理机），
**三类非活面无人同步**：①活文档（README/runbook/模板）仍写旧真相；②仓内清单与
生产外带项漂移；③代码默认值/SQL种子/测试外脚本把「当时为真的字面」冻结成默认值。
文档一旦含可执行字面（IP/env名/服务名/路由表），它就是施工的输入——错文档=错代码。

## 处置

- **查**：签名式 grep（死IP/错env名/错服务名/死拓扑）+ 每条对照生产实判定
  （DB/env/compose/探针/表新鲜度），区分真化石 vs 历史引证——历史件不篡改加横幅。
- **修**：活文档改写真值；退役文档加「已取代/superseded」横幅指向现行 SSOT；
  代码默认值/SQL 种子纠部署中立值；游离生产脚本收编 git。
- **防再生**：`scripts/check_fossil.sh` pre-commit 三门——死字面扫描（签名表）
  + cex-proxy 路由三方对账（config.json==defaultConfig==README）+文档同步耦合
  （diff 内 fossil-nosync 显式豁免留痕）。纪律 SSOT=practices.md「文档同步纪律」节。
- **签名表维护律**：生产事实变更先改 `scripts/fossil-signatures.conf` 再改代码文档；
  新签名必附死因。

## 证据

- check: `docker exec arb-postgres psql -c "SELECT max(ts) FROM fin_cex_radar"` → 9-26 13:17Z（37h 断更）；`grep BINANCE_PROXY_URL bin/*-run.sh` → 两 wrapper 同型死字面
- check: `curl -H "X-Proxy-Token: $TOK" https://cex.myfxlogs.org/okxws` → 400（透传=路由实存）；`git grep okxws tools/cex-proxy/` → 0（仓内缺）=清单漂移实证
- check: `./scripts/check_fossil.sh` 注入死IP行 → FAIL；行内加 fossil-allow → OK；staged main.go 无 README → 耦合 FAIL
- ref: D-538 扩围稿 @22d6aaef；化石清剿批1-3 ff4b0e59/691544b2/889ad45c

## 反证条件

- check: fossil-lint 上线后若仍出现「文档化石喂出代码化石」案例 → 签名表盲区，需扩签名而非加豁免
- falsifies_when: 门禁全绿状态下 tracked 活文档检出运维死字面且无豁免标记

## 适用边界

- 时点记录类（archive/LOG/decisions/施工提示词/审计/报告/经验库条目）天然引证化石
  不受此门——它们记当时真相；判别线=「读者会不会拿它当现行操作输入」。
- 测试 fixture 死字面不拨号属固件豁免；代码默认值/生产脚本/SQL 种子是真化石。
