---
---
schema_version: 2
id: trongrid-405-readonly-metadata-endpoints
kind: pitfall
risk: high
status: verified
keywords: [TronGrid, 405, getreward, getwithdrawreward, getbrokerage, listwitnesses, Tronscan, allowance, 奖励, brokerage]
scope: [TRON 主网, TronGrid API, 奖励监控, SR 元数据]
summary: TronGrid 网关禁用 getreward/getwithdrawreward/getbrokerage 三个奖励/分成查询端点——POST 返回 405 "HTTP method POST is not supported by this URL"(Jetty 层),GET 404,带 bogus API-Key 同样 405(非 key 门控,是端点下线);对照端点 getburntrx POST 200 证明 POST 通路正常。按官方文档集成这三个端点会直接失败。替代:未领投票奖励=getaccount.allowance(latest_withdraw_time 与最后领取交易同刻实证);SR name/brokerage=Tronscan /api/witness(免 key,listwitnesses 本身无 name/brokerage 字段)。
first_seen: 2026-09-03
last_verified: 2026-09-03
skills: []
evidence_refs:
  - type: doc
    ref: docs/TRX质押-P0侦察报告-2026-09-03.md §3#8/§4.1/§4.2
  - type: server_fact
    source: rpc
    query: POST api.trongrid.io/wallet/getwithdrawreward {"address":"<owner>"} → 405; POST /wallet/getburntrx {} → 200; GET apilist.tronscanapi.com/api/witness → 200
    observed_at: 2026-09-03T05:20:00Z
    server_revision: mainnet
    response_hash: ""
  - type: decision
    ref: D-210
---

## 事实/症状

按 java-tron/TronGrid 文档以 POST 调 `/wallet/getreward`、`/wallet/getwithdrawreward`、`/wallet/getbrokerage`,一律 405(`<title>Error 405 HTTP method POST is not supported by this URL</title>`,Jetty 错误页);换 GET 得 404;请求头带 bogus `API-Key` 仍 405。同一会话 `/wallet/getburntrx` POST 返回 200,排除"POST 方法整体不可用"。另:`/wallet/listwitnesses` 200 可用,但条目字段仅 address/voteCount/url/totalProduced/totalMissed/latestBlockNum/latestSlotNum/isJobs,无 name、无 brokerage。

## 根因或策略解释

TronGrid 网关在路径层下线了这三个查询端点(405=路由注册不含 POST),文档页仍在,形成"文档有效、端点失效"的陷阱;API key 只影响配额不影响路由。真 key 下是否恢复未验证(无有效 key 可测)。

## 处置

- 未领投票奖励:用 `getaccount.allowance`(sun,÷1e6),跨验=latest_withdraw_time 与链上最后一张 WithdrawBalanceContract 交易时刻吻合;注意 allowance 按维护期窗口量子化更新(非逐块)。
- SR 名称/brokerage/年化:Tronscan `GET apilist.tronscanapi.com/api/witness?sort=-voteCount&limit=N&count_all=true`(免 key 200;注意 count_all 会无视 limit 返回全量)。
- 历史领取事件:扫 `/v1/accounts/{addr}/transactions` 的 WithdrawBalanceContract(该合约无金额参数,领取金额用 allowance 前后差分还原)。

## 证据

- check: 三端点 POST/GET × base58/hex 地址矩阵 + bogus key + getburntrx 对照
- result: 405(POST)/404(GET)/405(bogus key)/200(对照),复测两次一致;listwitnesses 445 条字段清单核对无 name/brokerage
- ref: D-210(报告 a606951、审计 d1839eb)

## 反证条件

- check: 持有效 TronGrid API key 重调三端点
- falsifies_when: 有效 key 下任一端点返回 200,则"网关下线"定性错误(改为"免 key 被禁"),处置仍可用但文档应更新

## 适用边界

TronGrid 公共网关(api.trongrid.io,2026-09 主网实测);自建 java-tron full node 的 HTTP API 不受此网关限制(同端点应为 200,未实测)。Tronscan accountv2 需鉴权(401),witness 列表接口免鉴权为 2026-09 状态,可能随其 API 版本漂移。

> 2026-09-13 细化（D-295）：本条「未领投票奖励=getaccount.allowance」仅展示瞬时值近似成立；allowance 是 tx 物化值（claim 后长期 0），计量/差分用途必须用 tronscan rewardNum，见 `tron-allowance-materialized-vs-rewardnum`。
