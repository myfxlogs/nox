---
schema_version: 2
id: venue-ban-self-sustaining-consumers-must-backoff
kind: pitfall
risk: high
status: observed
keywords: [418 封禁自持, banned until, 共享 IP 权重, REST 退避闸, venue 限速, 续杯, X-MBX-USED-WEIGHT, 全局退避]
scope: [任何 venue REST 消费族（周期件/探测批/兜底件）, 共享出口 IP 的限速面, 418/429 封禁响应面]
summary: venue REST 封禁响应（418/-1003）报文含「IP banned until <epoch-ms>」明示期限，但消费面若无感知闸则每拍照发 REST 调用=撞击或续杯=封禁自持永不自然解禁。实证：binance 生产七件（capitalview/premiumIndex/dualSide/account-mon/positions/depth/reconcile/listenKey）封禁窗内 20min×223 撞 vs banGateRT 闸部署后近 12min 零撞自然解禁+funding 复流。修法=venue 级封禁状态件（报文解析 T→全消费件 T+margin 内短路 skip，一轮一 log 汇总非逐件刷屏，429 轻退避同机制），装配点=共享 transport/client 层单闸禁逐件复制。
first_seen: 2026-10-19
last_verified: 2026-10-19
skills: [bn-market, bn-userdata-ws]
evidence_refs:
  - type: file
    ref: internal/platform/httpx/ban_gate.go（banGateRT 单闸——banned-until 解析+ErrBinanceBanned 短路+429 轻窗）
  - type: commit
    ref: 69f38418d（实施）/ 62a290568（并入部署——收口注记#79）
  - type: sg
    ref: SG docker logs 10-19——封禁期 20min×223 撞（闸前）vs 部署后近 12min 零撞自然解禁+funding 复流 59s 鲜
  - type: doc
    ref: docs/handoff/STATE.md（binance 418 IP 封禁复盘票行——三次封禁实录+禁批跑纪律）
---

## 事实/症状

- venue 封禁响应（binance HTTP 418 + `{"code":-1003,"msg":"...IP(...) banned until <epoch-ms>..."}`）**报文自带期限**——消费面本可读出「封到几点」。
- 但无感知闸时：封禁期每个周期消费件照旧发 REST→venue 返回期限不一（各调用报回 ~200-400s 前瞻值不等）→每次调用可能顺延封禁（**续杯**）→封禁窗永不自然结束。
- 实证对照：闸前 20min×223 撞（capitalview 等七件每拍照发）；banGateRT（httpx 单闸）部署后闸窗内全短路 `ErrBinanceBanned`→封禁自然到点解禁→近 12min 零 418 零闸击+funding 复流。
- 泛化风险：该病灶**非 binance 独有**——任何带「期限报文+共享 IP 权重」的 venue REST 面同构；探测批（bnprobe 族）与生产周期件共享预算互烧时形态加重（10-19 三次封禁实证——探测批跑穿生产配额）。

## 判定/复现

- `docker logs` grep `http 418` 频次——封禁窗内仍高频撞击=续杯自持（解禁时间戳永不前移）。
- venue 报文 `banned until <ms>` vs `date`——封禁持续时长超限（binance 常见窗分钟级）而无收敛趋势=消费件在续杯。
- 入闸判别：REST 调用面 grep 有无统一 transport 层闸件——逐件自己 catch 418 不置全局戳=病灶面存在。

## 规避/修复

- **闸径单源**：共享 transport/client 层注入 banGate（本仓=`internal/platform/httpx/ban_gate.go` `banGateRT` 最外包 failoverRT——响应层语义先于传输层尝试）；禁逐消费件复制。
- **期限实读**：解析 `banned until T`→闸记至 T+margin（长戳取严）；期限未携=保守短窗。
- **skip 语义**：如实跳过+留痕（`ErrBinanceBanned` 可识别）非吞；入闸/放行状态变迁各一记非逐件刷屏。
- **429 轻退避同机制**（Retry-After 未携=固定短窗，指数窗无 venue 实据不造）。
- **探测批纪律**：能力探测=共享预算配额内跑（USED-WEIGHT 头实读预节流——bnprobe weightGate 件在案 @5ef38d7f4/24a3ab8ca）+批间冷却+封禁期禁跑。
