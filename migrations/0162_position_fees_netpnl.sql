-- 0162 D-599：手续费全链采集 + 盈亏净额化。
-- positions.fees_usd = 该仓全腿费用合计（实采/估算混合按 fee_quality 标）；
-- positions.fee_quality 枚举 actual/estimated/mixed/none——估算成分显式可辨（禁静默）。
-- realized_pnl 语义改净额 = 价差 − fees_usd（毛值可由 +fees_usd 反推，不另存列）。
-- 存量历史行不回写（S3 脚本 tools/cleanup/ 人工回标）；新仓由 ClosePosition 写净额。
ALTER TABLE positions ADD COLUMN fees_usd double precision;
ALTER TABLE positions ADD COLUMN fee_quality text;
