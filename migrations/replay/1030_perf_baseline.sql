-- 1030_perf_baseline.sql —— D-535（业主令 10-15）：业绩基线锚 $11,317 起算 +
-- 历史业绩序列归档（行不删不改值只标 archived）。D-534 本金口径=USDT+USD。

-- 基线锚落 app_config（D-536 总览页读面契约键名 baseline_usd/baseline_at）；
-- 值写死——改须新 D#。
INSERT INTO app_config (key, value, note) VALUES
 ('baseline_usd', '11317', 'D-535 业绩基线=10-15 实拍 USDT+USD 本金（可动用10949+锁定368：binance DCI 358 + okx 结构化 10）'),
 ('baseline_at', '2026-10-15T00:00:00+08:00', 'D-535 基线时戳（BJ 历日零时口径，与 fin_nav_daily.day_start 同族）'),
 ('baseline_ccy', 'USDT+USD', 'D-534 本金口径——非 U 币种涨跌/计息不入业绩，走披露行'),
 ('perf_flow_provisional', 'false', 'D-535④ 净流扣减依赖 io+划转台账（D-539 已落）；需回标 provisional 时置 true'),
 ('NOX_CAPITAL_BASE_USD', '11317', 'D-535⑤：D-030 年化分母换本金锚（原缺省 10000）')
ON CONFLICT (key) DO NOTHING;

-- 历史序列归档标记（可查、不参与当前指标）。
ALTER TABLE fin_nav_daily ADD COLUMN IF NOT EXISTS archived boolean NOT NULL DEFAULT false;
ALTER TABLE income_events ADD COLUMN IF NOT EXISTS archived boolean NOT NULL DEFAULT false;
UPDATE fin_nav_daily SET archived=true WHERE day_start < '2026-10-15T00:00:00+08:00'::timestamptz;
UPDATE income_events SET archived=true WHERE ts < '2026-10-15T00:00:00+08:00'::timestamptz;
COMMENT ON COLUMN fin_nav_daily.archived IS 'D-535：基线前历史行归档（口径污染——LDUSDT 双计+DCI 漏读+币持仓混入；可查不入指标）';
COMMENT ON COLUMN income_events.archived IS 'D-535：基线前历史行归档（同上口径污染；可查不入业绩指标）';
