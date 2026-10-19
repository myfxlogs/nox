---
schema_version: 2
id: mt-canonical-normalize-consumer-side
kind: pitfall
risk: high
status: observed
tags: [xasset, canonical, monitor-pair, false-positive]
---

# MT canonical 归一只在簿侧做、消费侧漏接——第三例（US500/TRXUSDT 同族）

## 病灶

簿 pair 键=`cexCanonical + "-" + bareOf(mtCanonical)`（`bareOf`=裁 USDT→USD 尾，tools/xasset-monitor/pairs.go:80）。**消费侧拿组合行 canonical 直拼 pair 键=查无行假阳**。三例在案：
1. US500@15032313 → MT 归一 SPX500（equiv 注册救回，us500-equiv 票）；
2. 同票 s2bridge 落 `canonical_sell` 吃 broker 原名不规范化（上游根因票候选）；
3. **本例（10-19）**：`comboBookTriple` 直拼 `CanonicalSell+"-"+CanonicalBuy`→查 `TRXUSDT-TRXUSDT`，簿实写 `TRXUSDT-TRX`→前提塌三发全假阳（armed#19 在列）。修：`mtBareCanon` 归一（oppmatch/regime.go，`cc4128b`）。

## 判定

消费 `fin_xasset_monitor`/簿对键时，pair 第二段**必过与写入侧同款归一**。新消费面起手先对拍一行实据（`SELECT pair ... LIMIT`）再写谓词——别信 canonical 字段「应该长什么样」。

## 同族更深根因

组合行 canonical 字段存的是**上游原值**（s2bridge/手工行不保证已归一）——归一纪律分散在每个消费点是结构性缺陷，长期解=写入侧归一一次（另案票候选）。
