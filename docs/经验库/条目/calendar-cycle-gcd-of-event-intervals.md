---
schema_version: 2
id: calendar-cycle-gcd-of-event-intervals
kind: strategy
risk: medium
status: verified
keywords: [周期设计, 日历节拍, funding_interval, GCD, 结算节奏, 4h, carry universe, 扫描器]
scope: [arb-core, scan, carry universe, 定时批处理设计]
summary: 周期性批处理节拍应取下游事件间隔分布的 GCD（现值 4h={1h,4h,8h} 分布的最大公约数）——更快=两结算点间数字不变白跑，更慢=欠采样短周期品种错过结算事件
first_seen: 2026-09-25
last_verified: 2026-09-25
skills: [事实核验]
evidence_refs:
  - type: db
    ref: "facts 表 funding_interval 分布：1h×1 / 4h×123 / 8h×123（venue+symbol 去重）"
---

## 现象

carry universe 扫描器 4h 周期（wire_scan.go:139）曾被质疑「为什么是 4h」——实证：`funding_interval` facts 实值分布 1h(1品种)/4h(123)/8h(123)，4h 是主流品种结算间隔的最大公约数。

## 根因或策略解释

carry 经济量（accrued/funding 收取）只在**结算边界**跳变，两点之间是恒定直线。扫描周期设计目标=「不错过任何结算事件的最粗粒度」：

- 比 GCD 慢（8h）：4h 品种错过一半结算点——accrued 晚一拍、逆向退出晚一轮止血；
- 比 GCD 快（5min）：两结算点间数值不变，重算=零信息白跑（且该轮是全宇宙 24h 窗 join 的重活）；
- GCD 本身（4h）：8h 品种的每个结算点都落在 4h 周期上，两群体全覆盖；唯一 1h 品种轻微欠采样（最坏晚 3h），发现层可接受——执行层精度靠事件流不靠此环。

## 处置

- 定日历周期时先拉下游事件间隔分布取 GCD，不按延迟焦虑定；
- 防御哨兵（可选）：扫描到 min(funding_interval) < 当前周期且样本>阈值→warn 提示周期重评；
- 「观察点=下轮复查」类运维断言须先确认该轮次是唯一触发源（本例步进链全挂在 4h 环+启动轮，重启=等价触发）。

## 证据

- check: `SELECT value/3600 h, count(DISTINCT venue,symbol) FROM facts WHERE kind='funding_interval' GROUP BY 1`
- result: {1h:1, 4h:123, 8h:123}；GCD=4h；审计 KEEP 清单「4h 日历节拍=实证正确保留」
- ref: wire_scan.go:20/139、carry_universe.go:3 注释「每 4h（结算节奏）」

## 反证条件

- check: venue 大面积改结算档或新接品种 min interval <4h
- falsifies_when: 分布变为如 {2h:50+,8h:...} → 周期应按新 GCD 重评

## 适用边界

「值只在离散事件点跳变」的批处理场景（结算/快照/排名）；latency-sensitive 控制环不适用——那类走事件驱动非周期。
