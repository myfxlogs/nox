---
schema_version: 2
id: venue-mirror-balance-double-count
kind: pitfall
risk: critical
status: verified
keywords: [LDUSDT, LD*, binance, 镜像余额, 双计, NAV, earn 回显, -6018, spot free]
scope: [reconcile, cex, binance, earn, NAV, snapshot]
summary: binance /v3/account 把 Simple Earn 活期持仓以 LD* 前缀镜像币形式回显现货余额——计为 spot free 后与 earn 独立读面双计，并驱动虚假可用余额下单被 venue -6018 拒（10-15 实证 spot/USDT=1088.8 全为 LDUSDT 假影，真 USDT=0）
first_seen: 2026-10-15
last_verified: 2026-10-15
skills: [生产取证]
evidence_refs:
  - type: server_fact
    ref: SG 快照 spot/USDT=1088.80430769 全为 LDUSDT 回显；业主 App 现货 USDT=0；earn/USDT=1240.13 为同一笔钱
  - type: server_fact
    ref: fin_dci_order #31 EARN 1088.80 failed——申购按假 free 下单被 binance -6018 asset not enough 拒
  - type: decision
    ref: decisions.md D-528/D-529（LD* 排 free/spot 口径裁定）
repro_or_symptom: venue 账户查询把理财持仓以派生/镜像资产回显主钱包余额→计为可用余额后与理财钱包独立读面双计；可用余额虚高→按之申购/下单被 venue 余额不足拒单
root_cause: binance /v3/account 响应含 LD* 前缀资产（LDUSDT=活期理财镜像），它不是可动现货；earn 读面另端点独立返回同一份钱
fix_and_prevention: ①LD* 前缀整体排除出 spot free 与 spot equity（earn 读面已独立收录）；真 LD* 币（LDO 等）另走显式白名单②spot freeMargin 只认裸 USDT③新接入账户类端点先区分「钱包真值」与「派生镜像/回显」列——凡同名双端点可见的资产必须单一归属④对账闭环（残差票）是这类双计的根治面
notes: 检测信号：某币种余额突跳且与另一钱包等量同步=疑似镜像；-6018/asset not enough 拒单但账面余额充足=镜像假 free 典型症状
---

## 现象

binance spot `/v3/account` 把 Simple Earn 活期持仓以 `LD*` 前缀镜像币回显：LDUSDT=1088.8 对应 earn 池里的同一份钱。计为 spot free 后 NAV 双计 1089，且驱动 earn 申购单按假可用额下单被 -6018 拒。

## 根因或策略解释

venue 账户端点返回的「余额」混含真钱包值与理财子产品派生回显，无字段标志区分；earn 端点又独立返回同份钱——两个读面必须人工单一归属。

## 处置

LD* 前缀整体排 spot free/equity（已修，@8b434a4a3）；spot freeMargin 只认裸 USDT；真 LD 系资产（LDO）走显式白名单。根治=残差对账闭环票（equity 等式残差告警）。

## 证据

- check: SG `fin_venue_account_snapshot` spot/USDT vs App 实拍
- result: 系统 1088.80 vs App 0.05；修复后 0.05 对平
- ref: commit 8b434a4a3 / 9f4f0263d；D-528/D-529

## 反证条件

- check: binance 若改 API 不再回显 LD*，排除规则是否误伤真实产
- falsifies_when: venue 文档/实测证明 LD* 前缀资产是可动现货余额（当前实测证伪：LDUSDT 不可提不可用于下单）

## 适用边界

binance spot×earn 复合读面；okx/gate 同型「子产品回显主余额」需随接入核查（okx 结构化票已带此检项）。
