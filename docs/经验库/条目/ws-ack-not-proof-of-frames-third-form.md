---
schema_version: 2
id: ws-ack-not-proof-of-frames-third-form
kind: pitfall
risk: medium
status: observed
keywords: [ack≠存活, WS 订阅, ack 帧, 业务帧, MarkFrameOK, Mark dial 记账, venue 静默裁, epoch 裁断]
scope: [任何 venue WS 订阅面存活判定, wsrunner/dephealth 类健康记账, 首帧证 vs 业务帧分域]
summary: 「ack≠存活」第三形态：WS 订阅 ack 收讫+连接就绪≠业务帧在流——venue 对连接 epoch 概率性静默裁断（binance markPrice/ticker 聚合流族实拍：ack{result:null} 后零帧 8s+/10min 窗零帧，watchdog 判死重连复帧=venue 常态节拍）。判别法器=「帧戳前移」非 ack 收讫非连接活：dephealth 须分域记账——`Mark(key,ok)`=连接/dial 事件戳、`MarkFrameOK(key)`=业务帧到戳（节流 1/min/键），两键同核方知真死活。存活判据错配=「连接活=健康」假设破，REST 兜底面被绿假象遮住。
first_seen: 2026-10-18
last_verified: 2026-10-19
skills: [bn-userdata-ws]
evidence_refs:
  - type: file
    ref: internal/infra/dephealth/dephealth.go（Mark vs MarkFrameOK 分域——frameMarkAt 节流戳独立）
  - type: file
    ref: internal/data/collect/exchange/wsrunner.go（wsrunner 生命周期日志四行——dial/subscribed/first frame/ping-write-fail 取证面）
  - type: commit
    ref: 75f7f562d（bn-zombie 取证+生命周期日志件）/ 27e19f500（dephealth MarkFrameOK 件）/ 6a0ed2b（user-ws 接线）
  - type: sg
    ref: SG 直探 10-18——`!markPrice@arr` 双 egress ack{result:null}+零帧 8s+ vs `!bookTicker` 同窗活
---

## 事实/症状

- 形态一（已知）：venue 收订阅回 `{"result":null,"id":N}` ack 但零推帧——ack 只证握手可达。
- **形态三（本条目）**：连接就绪+订阅 ack 收讫+**业务帧零推**——venue 按连接 epoch 概率性静默裁断（binance `!markPrice@arr`/`!ticker@arr` 聚合流族实拍：双 egress 同形态 ack+零帧=venue 全局裁断非 IP/径病灶；封禁期 venue 对 WS 推送亦静默——决策席 10-19 裁 venue-WS-throttle 不立案但形态记实）。
- 病灶危害：dephealth `healthy` 绿与帧流脱钩——`Mark(ok)` 只在 dial/err 记连接戳，帧死流时健康面误标绿，REST 兜底面被遮（funding 面曾靠 REST 兜底遮住 markPrice 断流）。

## 判定/复现

- `dephealth` 键 `lastEventUnix` 龄期采样两次——钉死不前移=帧死（非看 `state` 字段）。
- `docker logs` `no business data frames for` 看门狗周期触发=帧流断但连接活的签名。
- ack 收讫≠证据——判别法器必用「帧戳前移」：裸 ws 探针对 venue 直订（ack+帧窗双记）定谳 venue 裁 vs 我方读环卡。

## 规避/修复

- **存活判据=帧戳前移**（MarkFrameOK 帧到记账，节流 1/min/键不刷注册表）——`Mark` 与 `MarkFrameOK` 分域喂入，健康面核查两键同核。
- **wsrunner 生命周期日志四行在案**（dial ok/subscribed N/first frame received/ping write failed）——僵尸三态可分域定界：dial 活+订阅讫+帧零=venue 断供形；+帧到+业务零=handler 形。
- **探针直对拍**=venue 裁断诊断正道：venue ws 流名失效=ack≠证据，必须帧实拍。
- **判死阈按频谱定**——1s 频谱流（`!markPrice@arr` 族 1000ms）无帧>60s=铁死非静默期（mktWSDataIdle=3min 降档在案 @ce1f3dfe3——10min 阈白送盲窗+funding stale 刷屏+418 期零兜底三损叠加）。
