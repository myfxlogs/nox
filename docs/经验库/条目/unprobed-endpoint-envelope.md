---
schema_version: 2
id: unprobed-endpoint-envelope
kind: pitfall
risk: high
status: verified
keywords: [响应信封, 顶层裸数组, json.Unmarshal, fixture 循环证据, TRONSCAN, apilist, witness, data 字段, 探针缺口, 测试全绿功能死]
scope: [采集器, 外部公开 API, 第三方 HTTP 端点, TRX]
summary: 设计只探针了多端点数据源中的一个，其余端点响应形状靠类型假设；测试 fixture 又从实现假设反向构造（循环证据）——测试全绿但 json.Unmarshal 对真实输入必败，功能死胎。防法=每个端点独立 curl 探针后才定 Go 类型+fixture 必须粘贴实抓样本。
first_seen: 2026-09-05
last_verified: 2026-09-05
skills: [事实核验, KB维护]
evidence_refs:
  - type: commit
    ref: 05f416d
  - type: log
    ref: docs/经验库/证据/tronscan-witness-toplevel-array-2026-09-05.log
---

## 事实/症状

D-231 TRX 投票 APR 采集器（05f416d）机检全绿、5 条单测全 PASS，但上线即死：TRONSCAN `/api/witness` 真实响应是**顶层裸数组**（445 行），代码按 `{"data":[...]}` 信封 unmarshal 必返错，`Poll` 每轮失败、零落库。同一 API 域的 `/api/vote` 却**有**信封（`{"totalVotes",data[]}`）——同域两端点形状不一致。

## 根因

①设计探针覆盖不完整：设计 §S1.2 只实测了 vote 端点，witness 形状未探针即写入 Go 类型假设，施工忠实执行假设；②fixture 从实现假设反向构造：测试 JSON 是手写的、与代码同盲区，断言「代码处理的输入」而非「服务器发出的输入」——循环证据让 5 条测试全绿；③「探针过该数据源」的覆盖感替代了「逐端点探针」的机械纪律（同 partial-corpus-blindspot 的「覆盖感」病，落到协议形状维度）。

**字段级复发（2026-09-05 当日，生产实抓）**：R1 修复只核真了信封层，R4 随即在生产抓到 `/api/vote` 的 `timestamp` 字段为**字符串**而代码/fixture 均按 int64——每轮 unmarshal 失败、current_rate 恒 0、投票年化行永不显示。教训升级：**探针要落到逐字段类型**（python 打印全字段 type 贴注释），信封/字段名/地址格式核过≠字段类型核过；未消费的字段同样会炸解析（timestamp 无下游消费但拦死整个 unmarshal）。**第三层教训（同日，复审指令自身）**：R4 修复指令中的示例值 `"1757054520000"` 是指令撰写人杜撰的毫秒纪元样式（日志只证明"是字符串"未显示值），实抓真值是 ISO-8601（`"2026-09-05T11:12:36.632+0000"`）——**示例值不实抓就是新杜撰**，下游（施工方注释）会忠实复制错误。

## 修法

多端点数据源：**每个端点独立 curl 实抓后才定类型**，设计文档逐端点记录形状证据；测试 fixture 必须含一段**实抓原文**（逐字字段名）作形状锚，禁纯手写；「数据源已核真」表述只在逐端点列全后才允许出现。

## 反证条件 / 适用边界

仅适用「外部 HTTP/WS 端点 → 结构化解析」场景；内部函数/私有接口无第三方形状风险，不适用。若端点响应带版本协商/多形态（如 Accept 头分支），需逐形态取证。

## 泛化判断

可晋升 ~/kb：任何 agent 接第三方 API 同构——「测过 A 端点 ≠ 摸过 B 端点」「fixture 手写=验证自己的想象」。与 [[server-facts-over-docs]] 互补：那条防「信文档不信服务器」，这条防「信部分探针不信全部端点」。随下次收工晋升。
