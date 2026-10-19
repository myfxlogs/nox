---
schema_version: 2
id: test-fixture-fixed-date-rolling-now-mix
kind: pitfall
risk: high
status: verified
keywords: [测试fixture, 固定日期, time.Now, 时间弹, 永久红, 假flaky]
scope: [测试编写, internal/research/scan, 一切含时间窗判据的测试, CI 闸门]
summary: 夹具混用两时间系（固定日期参考轴×time.Now 实体）——真实时间过临界点后时间窗判据恒不中→用例永久假红非 flaky；早绿晚红易误判环境差异。
first_seen: 2026-10-19
last_verified: 2026-10-19
skills: []
evidence_refs:
  - type: commit
    ref: 133d0869e
  - type: doc
    ref: internal/research/scan/paper_cfd_book_gate_session_test.go:30
  - type: server_fact
    source: ci
    query: gh run view 39247856378 --log-failed（11:56Z 首红）/39252534266（修后绿）
    observed_at: 2026-10-19T11:56:16Z
    server_revision: master b2d5421bb
    response_hash: ""
---

## 事实/症状

TestAnchorDead_SessionGate（锚死会话闸锚）自 2026-10-19T11:45Z 起 CI 恒红（用例③「会话表缺席锚死应照报」失败），连带后续 4 次 master push CI 全红；同日更早的本地全量与 CI（11:34Z 前）均绿——表象酷似 flaky 或 CI 环境差异。复测：本地 11:15Z 绿、11:47Z 红、干净 master 树同样红——与分支/合流/环境零相关，纯时间推移触发。

## 根因或策略解释

夹具把「参考时刻」写成固定字面量（sat=2026-10-17T12:00:00Z / sun=2026-10-18T23:00:00Z）而把「实体时间」写成 `EntryTs: time.Now().Add(-48h)`——判据 `now.Sub(EntryTs) > paperCfdMonitorMaxAge(15min)` 的 age=sat−(realNow−48h) 随真实时间单调递减，realNow 越过 sat+48h−15min（=11-19T11:45Z）后恒小于 15min → 锚死分支永不进入 → 用例③期望的告警永不出现。本质=夹具内混用两个不可换算的时间系；「绿灯快照」只是弹起前的时间窗口，一旦过临界点永久红（不像 flaky 可重试漂绿）。

## 处置

夹具全固定：EntryTs 改固定字面量（time.Date(2026,10,16,...)），使 age(sat)=36h/age(sun)=71h 相对参考轴恒定——三支路语义（休市豁免/开市照报/无表照报）不变、永久确定（修件 133d0869e）。编写纪律：时间窗判据的测试里参考轴与实体时间必须同源——要么全 time.Now() 派生（相对时间），要么全固定字面量（绝对时间）；固定字面量适合钉会话表等真实日历数据（周日窗），此时实体时间必须同样固定。

## 证据

- check: `go test ./internal/research/scan/ -run TestAnchorDead_SessionGate -count=1`
- result: 修前红（12:02Z 本地+CI 39247856378 同红）/修后绿（count=2 连跑）+CI 39252534266 全绿
- ref: 133d0869e（复审修件）；CI 红链 39247856378→39251620517 四连

## 反证条件

- check: 修后跨临界时刻复跑（realNow > 2026-10-19T12:00Z 后任意时刻）
- falsifies_when: 固定 EntryTs 后用例仍在特定真实时刻翻转红绿（存在夹具外的时间依赖，如产码内隐 time.Now 参与判据）→ 本条「夹具内两时间系混用即全因」结论失效
