---
schema_version: 2
id: cb-data-source-matrix
kind: pitfall
risk: high
status: verified
keywords: [可转债, 数据源, akshare, baostock, 集思录, 东财, 新浪, 幸存者偏差, 退市券, 僵尸行, 海外IP, push2]
scope: [cb-research, data-collection, backtest]
summary: 可转债全样本回测数据源地图与三陷阱——①新浪 spot 列表混入已退市券僵尸行，「在场」须用 EM 条款表 DELIST_DATE/强赎表独立过滤；②东财 push2 行情族海外出口不可达而 datacenter 族正常，可达性按端点族实测；③akshare 函数名跨版本漂移，调用前 dir() 清点。可行路径=转债日线（新浪，退市券历史完整）、正股（baostock）、转股价序列（集思录 adj_logs 逐券）、发行全集+条款（EM datacenter 1051 行）；0.93s/券
first_seen: 2026-09-01
last_verified: 2026-09-01
skills: [启动与检索, KB维护]
evidence_refs:
  - type: server_fact
    ref: 2026-09-01 本机实跑：spot 326 只含僵尸行；123004=铁汉（非搜特，代码归属以发行表为准）；搜特=128100 尾部 18 元/广汇=110072 尾部 45.77；老三板 404002 新浪/baostock 均无（回测以退市日价封底）
  - type: doc
    ref: docs/可转债轮动-P0可行性侦察报告-2026-09-01.md §1 数据源实测矩阵
repro_or_symptom: 直接用 spot 列表当 universe→退市券混入污染信号；海外环境直接假设东财接口可用→push2 全空；按旧教程函数名调用→AttributeError
---
