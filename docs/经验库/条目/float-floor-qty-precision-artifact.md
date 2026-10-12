---
schema_version: 2
id: float-floor-qty-precision-artifact
kind: pitfall
risk: high
status: verified
keywords: [float, floor, step, qty, 精度, -1111, Precision is over the maximum, 浮点伪影, binance, 下单拒单]
scope: [CEX 执行链, qty/price 规范化, Go float64, 任意 venue 精度过滤]
summary: Floor(q/step)*step 的浮点乘法伪影产出 1.4000000000000001（≠double 1.4），'f',-1 最短往返序列化输出 17 位有效数字超 venue 精度 → binance -1111 拒单；同源还有除法下溢（1137.4/0.01=113739.999…→Floor 掉 1）静默缩水。修法=epsilon+按 step 小数位 round 回干净值。
first_seen: 2026-08-28
last_verified: 2026-08-28
skills: [KB维护, 事实核验]
evidence_refs:
  - type: decision
    ref: D-127
  - type: doc
    ref: internal/dashboard/service_legs.go:171
  - type: server_fact
    source: postgres
    query: SELECT qty FROM position_legs WHERE position_id IN (380,383)
    observed_at: 2026-08-28T02:15:00Z
    server_revision: ""
    response_hash: ""
---

## 事实/症状

D-126 扩品种后 PERP_FUNDING 过夜 11 笔 6 LEGGED，全部 binance:perp 下单 `http 400 {"code":-1111,"msg":"Precision is over the maximum defined for this asset."}`。#380 BNBUSDT qty 串 = `1.4000000000000001`（17 位）；#383 DOTUSDT qty=1144.03 是合法数值但小数位 2 > venue qtyPrecision 1。

## 根因或策略解释

1. **乘法伪影**：`math.Floor(1.400639/0.01)*0.01 = 140*0.01 = 1.4000000000000001`——与 double 1.4 是不同浮点值；`strconv.FormatFloat(q,'f',-1,64)` 最短往返序列化被迫输出 17 位才能 round-trip → 超精度拒单。
2. **除法下溢**（潜伏）：`1137.4/0.01 = 113739.99999999999` → Floor=113739 → qty 静默缩水成 1137.39。
3. **硬编码 step 表失真**（叠加因素）：猜测值 DOT=0.01 ≠ 实际 0.1（1137.4 成交/1144.03 被拒交叉实证）。

## 处置

`floorToStep(qty, step)` 纯函数：`n := Floor(qty/step + 1e-9)`（epsilon 防下溢）+ `dec := stepDecimals(step)`（字符串解析小数位）+ `Round(n*step*10^dec)/10^dec`（round 防乘法伪影）。step 元数据权威化（运行时拉 exchangeInfo，代理 403 拉不到就直连官方域）+未知=0 fail-closed 拒单。

## 证据

- check: 生产回归——修复部署后同品种同数量 BNBUSDT qty=1.4 双腿 FILLED（修复前同值被 -1111 拒）
- check: 单测 `floorToStep(1.400639, 0.01)==1.4 && FormatFloat('f',-1)=="1.4"`、`floorToStep(1137.4, 0.01)==1137.4`（非 1137.39）
- ref: D-127、commit b8ea963、docs/binance-perp精度双缺陷修复-D-127-施工提示词.md

## 反证条件

- check: 对 step∈{1,0.1,0.01,0.001} 全档随机 qty 穷举 `FormatFloat('f',-1)` 输出小数位
- falsifies_when: 任一输出小数位超过 step 小数位（伪影未根除）

## 适用边界

Go float64 + 数值序列化进带精度过滤的 venue（binance -1111 最典型）。MARKET 单无 price 参数时只有 qty 一处；venue 元数据经代理不可得时 fallback 表必须「实测对齐+未知 fail-closed」，禁止「未知默认 0.01」猜测。
