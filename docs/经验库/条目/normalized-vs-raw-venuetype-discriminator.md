---
schema_version: 2
id: normalized-vs-raw-venuetype-discriminator
kind: pitfall
risk: high
status: verified
keywords: [okx, unified, venue_type 归一, rest_calib, okx_event, 快照振荡, totalEq, eqUsd, USDT 伪标, NAV flap]
scope: [account_monitor, capitalview, snapshot, okx, unified, normalize]
summary: 同键双写源判别口径不一——写行标签用归一 venue_type（okx spot/perp→unified），分币判别却按原始 VenueType——perp 配置行落单行路径把 ΣeqUsd 总额挂 USDT 标签写 unified 键，与事件径分币尘埃行互踩，NAV 池 flap ±43（10-15 SG 实拍 rest_calib 19.95 vs okx_event 3.4e-9）
first_seen: 2026-10-15
last_verified: 2026-10-15
skills: [生产取证]
evidence_refs:
  - type: server_fact
    ref: SG fin_venue_account_snapshot okx:unified/USDT 交替写 rest_calib=19.95~43.7 与 okx_event=3.4e-9（5s 级 flap）
  - type: server_fact
    ref: 修复部署后 rest_calib 改出 PENGU/ETC 分币原生行，USDT 行收敛 okx_event 尘埃值，flap 消失
  - type: decision
    ref: decisions.md D-543R1/R2（判别改归一池型；基线翻案回滚 11,317）
repro_or_symptom: 同一快照键（venue,venue_type,currency）两个写者交替写差异巨大的值；本金池随写序跳变；venue 账户明明只有非 U 币却出现大额 USDT 行
root_cause: 归一化（写标签）与判别（走路径）用了两个不同口径的 venue_type——NormalizeVenueType 把 okx spot/perp/futures 钉成 unified，但判别分支读原始 VenueType='perp' 落单行路径；且单行路径把 acct.Equity（跨币 ΣeqUsd 总权益）挂在 acct.Currency='USDT' 标签下=全币折 U 伪账
fix_and_prevention: ①凡写侧归一 venue_type 的位置，其判别分支必须用同一个归一值（snapshotRows/capitalview 均改 vt 判）②分币池（earn/spot/unified/funding/futures）单行落库只能写本币切片（FreeQtyByCcy+Holdings 本币 eqUsd），缺证如实零写禁回落总额③perp/delivery 等单币池 Equity 即本币真值保留总额径④新写者接入既有快照键前先查同键既有写径的口径（src 列即取证入口）
notes: 排查口诀：同键行值数量级跳变→先按 src 列分组，两个写者口径对撞是首嫌；「带 # 号 raw type」与「归一 type」混用是归一化改造期高发坑
---

## 现象

`fin_venue_account_snapshot` 的 `okx/unified/USDT` 键在 ~19.95（rest_calib）与 3.4e-9（okx_event）之间以 5 秒周期振荡——NAV 本金池随最新行 ±43 跳变。业主确认该户全是非 USDT 币（DOGE/PENGU/ETC），真 USDT 仅尘埃。

## 根因或策略解释

两个写者口径对撞：

- **事件径**（okx_event）：WS balanceUpdate 按币种写真值 availBal，USDT=尘埃 ✓
- **REST 径**（rest_calib）：`snapshotRows` 按**原始** `acct.VenueType` 判分币——okx 交易侧配置行原始 type=perp，不在 multiCcy 名单 → 单行路径写 `acct.Equity`（=/account/balance 全 details 的 ΣeqUsd，含全部币折 U 值），但行标签用归一 `vt='unified'` + `currency='USDT'` → **全币总权益被错标成 USDT**。

## 处置

- monitor/capitalview 两径判别统一改归一 `vt`（multiCcyVenueType 同名单）
- 分币池单行路径禁写总额——本币切片（availBal/本币 eqUsd）或如实零
- perp/delivery 单币池（binance perp 1344 例）总额径保留
- 钉死测试：`TestSnapshotRowsOkxPerpUnified` + `TestD543_CalibSnapshotUnifiedPerpSlice`

## 证据

- check: SG snapshot 表 src 分组对拍（19.95 vs 3.4e-9 交替行）
- check: 部署后 rest_calib 改出 PENGU/ETC 分币行，USDT 键收敛尘埃
- code: `account_monitor_snapshot.go snapshotRows`、`capitalview.go` calib 分支
