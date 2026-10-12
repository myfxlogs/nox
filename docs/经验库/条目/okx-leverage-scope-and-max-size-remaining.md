---
schema_version: 2
id: okx-leverage-scope-and-max-size-remaining
kind: pitfall
risk: medium
status: verified
keywords: [OKX, set-leverage, leverage-info, mgnMode, posSide, cross, isolated, max-size, 杠杆档位, 剩余容量]
scope: [okx, perp, 账户配置, 保证金预检]
summary: OKX 杠杆设置按 (instId, mgnMode, posSide) 分格——调 isolated 不影响 cross 下单路径；max-size 返回值=「当前杠杆设置×权益−已占保证金」的**剩余可开张数**，持仓后会下降（非容量变小）；venue 上限在 instruments.lever（XAU/BTC-USDT-SWAP=100x）
first_seen: 2026-09-24
last_verified: 2026-09-24
skills: [KB维护, 事实核验]
evidence_refs:
  - type: server_fact
    source: cex
    query: GET /api/v5/account/leverage-info?instId=XAU-USDT-SWAP&mgnMode={cross,isolated} → 四格独立 lever 值；max-size 持仓前 1052→开仓后 737
    observed_at: 2026-09-24T18:00:00Z
    server_revision: ""
    response_hash: ""
  - type: server_fact
    source: cex
    query: GET /api/v5/public/instruments?instId=XAU-USDT-SWAP → lever=100（venue上限）
    observed_at: 2026-09-24T18:00:00Z
    server_revision: ""
    response_hash: ""
---

## 事实/症状

业主调 XAU-USDT-SWAP 杠杆 3→5x 后 max-size 仍返 1052 未变；另一次观测中 maxSell 从 1052 骤降到 737（疑似容量缩水）。

## 根因或策略解释

两件事各自成立：①`set-leverage`/`leverage-info` 按 (instId, mgnMode, posSide) 四格独立——第一次只改了 isolated 格，而下单/预检走 `tdMode=cross`（代码写死 cex_executor.go:125），cross 格仍 3x 故 max-size 不变；②`max-size` 是**剩余容量**语义=杠杆设置×权益−已占保证金，cross 调 5x 后总容量 ~1755 张，开仓 1016 张后剩 ~737——下降是仓位占用而非档位失效。hedge 模式长短侧也分格（posSide=long/short 各一值）。

## 处置

调杠杆须对准下单用的 mgnMode+posSide 格；诊断 max-size 下降先查持仓占用再疑档位。估算公式：maxSell≈权益×lever/(价格×ctVal)，need_lever≈当前lever×(目标张数/0.8/当前maxSell)（含 20% 安全垫口径）。

## 证据

- check: leverage-info 四格实拍（cross=3 isolated=5→cross=5 后 max-size 变化）；持仓后 max-size 1052→737 与占用 1016 张对账吻合
- result: cross=5 生效后预检通过、posID=502 开仓
- ref: D-429 部署取证 / pos502 venue 实录

## 反证条件

- check: 若 OKX 改为统一杠杆格或 max-size 改返「总容量非剩余」语义
- falsifies_when: 同 mgnMode 两 posSide 实测返回不同上限口径

## 适用边界

OKX SWAP hedge/long_short 模式；net 模式 posSide 维度可能退化。max-size 还随权益/标记价实时漂移（观测 1052→1054 同档位微动）——安全垫口径要留余量，贴线必翻。
