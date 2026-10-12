-- 0168 D-018 阶段a：position_legs.fill_kind——腿成交方式（maker|taker）。
-- 供 D-014 经济闸成本修正（EntryCostUSD/ExitCostEstUSD 按 fillKind 取 maker/taker 费率）；
-- NULL=旧数据/未采集（诚实缺省，econ 侧回退 taker 保守口径）。
ALTER TABLE position_legs ADD COLUMN IF NOT EXISTS fill_kind TEXT;
