---
schema_version: 2
id: composite-unique-key-all-dims-same-domain
kind: pitfall
risk: high
status: verified
keywords: [fin_venue_io, 复合唯一键, config_id, dedup]
scope: [pgstore, ledger, dedup, key-domain]
summary: 复合唯一键 dedup 须全维同域——fin_venue_io config_id 漏维（exec 0 vs 采集 19/17）=dedup 虚设（同族第 8 例）
first_seen: 2026-10-19
last_seen: 2026-10-19
---

## 复合唯一键 dedup 须全维同域——fin_venue_io config_id 漏维实证

- **实证**：earn-io 专项复审抓获——`fin_venue_io` 唯一键 (venue,config_id,direction,external_id)，exec `ioTransfer` 不写 config_id=0，采集器写 spot 配置行（binance=19/okx=17）→external_id 同为 `se-<id>` 也撞不上冲突键=dedup 形同虚设。实施自测双侧同夹具 cfgID 掩住真域差。
- **修法**：exec 侧反解 `ExchangeConfigID(venue, spot→earn 兜底)`（a19c9257b）+ConfigID 断言测试。
- **教训**：键域对齐族第 8 例——**复合唯一键的对齐不是 external_id 一列的事，是全部维度同域**。测试夹具必须镜像真域差（异 id 双侧种行），同夹具双行=测不出。
