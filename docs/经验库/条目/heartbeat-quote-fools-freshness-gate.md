---
schema_version: 2
id: heartbeat-quote-fools-freshness-gate
kind: pitfall
risk: high
status: verified
keywords: [心跳报价, freshness, 时间戳门禁, 价格冻结, 周末休市, LEGGED, freeze guard, 单腿]
scope: [exec, collect, autotrade, MT5]
summary: 时间戳新鲜的报价≠可交易——周末/停牌/断流残留时 broker 推心跳报价（价格冻结、时间戳新），只验时间戳的 freshness 门禁被骗过 → 冻结腿下单被拒=LEGGED；可交易真信号=窗口内发生过价格值变化，且首见不算变化（防重启冷启动骗线）
first_seen: 2026-08-30
last_verified: 2026-08-30
skills: [事实核验]
evidence_refs:
  - type: decision
    ref: D-155
  - type: commit
    ref: 46ecd9e
  - type: doc
    ref: internal/bus/quote_bus.go:63
  - type: server_fact
    source: postgres
    query: SELECT id,status,closed_at FROM positions WHERE id=464
    observed_at: 2026-08-30T02:54:27Z
    server_revision: ""
    response_hash: ""
  - type: decision
    ref: D-495 # 9-26 pos502 平仓侧同型实证：funding=0 触发 exit，MT5 腿周末拒单 OKX 腿已平 → 裸多；判据同源（tick 值变化=可交易），本票补齐决策/平仓/在途重试三路径
  - type: server_fact
    source: postgres
    query: SELECT position_id,status,reason,ts FROM autotrade_logs WHERE position_id=502 ORDER BY ts DESC LIMIT 5
    observed_at: 2026-09-26T00:47:00Z
    server_revision: ""
    response_hash: ""
---

## 事实/症状

周日（2026-08-30）XAUUSD 组合 #464 LEGGED：FxPro `GOLD365`（周末 7×24 品种）心跳报价时间戳新 → 骗过 D-104 freshness 时间戳门禁 → 开仓通过预检；对腿 Pepperstone `XAUUSD` 周末休市被 broker 拒单 → 一腿 FILL（SELL 0.01 @4474.02）一腿 FAIL，留下 $4.4K 名义裸空。

## 根因或策略解释

freshness 门禁只验「报价时间戳是否新鲜」，但 broker（尤其 MT5 demo/gateway）在休市/断流残留时仍推心跳报价：**时间戳持续更新、价格值冻结**。时间戳新只证明「连接活着」，不证明「市场可交易」。事故时序还有一个加重因子：重启冷启动后首条报价落地——若把首见当变化，防线同样被骗（#464 正是重启后 swap 冷启动完成即开仓）。

## 处置

执行侧加 freeze guard（D-155）：QuoteBus 按 (canonical, venueKey) 维护 `lastChangeAt`——**首见不算变化、同值心跳不更新、bid/ask 任一变化才记时刻**；开仓前双腿各自校验「窗口内（默认 10m）发生过价格值变化」，任一腿冻结（无条目或超窗）→ 整组 abort（LEGGED 零容忍，单腿不降级），≤0 可禁用。分层：freshness=采集侧时间戳门禁，freeze guard=执行侧价格变化门禁，心跳报价骗前者、骗不了后者。

## 证据

- check: `go test ./internal/bus/ ./internal/dashboard/ -run 'LastChange|FreezeGuard'`
- result: 9 例全绿（首见不算变化/同值心跳不更新/价格变化更新/per-key 隔离/冻结腿 abort/双腿活跃放行/禁用开关/SL 透出 2 例）
- ref: D-155 复审（2026-08-30），生产 #464 事故为反例实证：冻结腿被拒、活跃腿放行的分界正是价格是否变化

## 反证条件

- check: 观察 freeze guard 日志的误拦率与漏拦案例（周一开盘后）
- falsifies_when: 若出现「窗口内价格有变化但仍不可成交」或「主流高流动性品种开盘后被频繁误拦」→ 「价格变化=可交易」映射不成立或 10m 窗口失配，需引入成交量/点差宽度维度

## 适用边界

- CFD/MT 场景（周末休市、断流残留心跳）为原发场景；CEX 7×24 价格常变不受影响。
- 误伤面：低流动性交叉盘 10 分钟无 tick 变化会被误拦（fail-closed，错失机会不断钱，已接受）。
- 仅防「开仓侧」；平仓路径不经此预检（设计如此）。
- 仅本项目适用（与具体 broker 心跳行为相关），不晋升 ~/kb。
