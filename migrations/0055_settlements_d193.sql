-- 0055_settlements_d193.sql - D-193-A：position_settlements 净 PNL 字段扩展
-- 设计 SSOT: docs/步骤3资本事实与经济性系统-详细设计.md §3.6
-- 语义：
--   price_pnl_usd     = 价格差组件（原 realized_usd 语义迁移，保留 realized_usd 兼容旧查询）
--   cashflows_net_usd = swap+funding+commission 组件合计
--   net_realized_usd  = price_pnl_usd + cashflows_net_usd（正式净 PNL，正式统计唯一消费值）
--   pnl_quality       = actual/estimated/legacy/unknown
--   completeness_status = complete/skipped/unknown/failed
--   stats_status      = pending/applied/retryable/skipped
-- legacy 行：新字段 NULL，pnl_quality 标 legacy（迁移时回填）。

ALTER TABLE position_settlements ADD COLUMN IF NOT EXISTS price_pnl_usd           double precision;
ALTER TABLE position_settlements ADD COLUMN IF NOT EXISTS cashflows_net_usd       double precision;
ALTER TABLE position_settlements ADD COLUMN IF NOT EXISTS net_realized_usd        double precision;
ALTER TABLE position_settlements ADD COLUMN IF NOT EXISTS pnl_quality             text;
ALTER TABLE position_settlements ADD COLUMN IF NOT EXISTS completeness_status    text;
ALTER TABLE position_settlements ADD COLUMN IF NOT EXISTS stats_status            text;
ALTER TABLE position_settlements ADD COLUMN IF NOT EXISTS stats_updated_at        timestamptz;
ALTER TABLE position_settlements ADD COLUMN IF NOT EXISTS settlement_model_version integer;
ALTER TABLE position_settlements ADD COLUMN IF NOT EXISTS settlement_event_key    text;

-- D-193 审计 P2-2 → D-249 S5 更正：原 BEGIN/COMMIT 经 pool.Exec 每语句独立连接，
-- 事务包裹为零（pgxpool Release 时 TxStatus≠'I' 即 Destroy）——注释宣称的保护是假的。
-- D-249 S5：migrate.go 已改为 per-migration 单连接事务（applyMigrationTx），
-- 本文件全部语句在同一个 BEGIN/COMMIT 内执行——SQL 内禁再嵌 BEGIN/COMMIT
-- （pgx 事务内 BEGIN 产生 WARNING 被忽略，COMMIT 会提前提交外层事务）。
-- legacy 回填：已有行的 realized_usd → price_pnl_usd，pnl_quality = legacy
UPDATE position_settlements
SET price_pnl_usd = realized_usd,
    pnl_quality = 'legacy',
    completeness_status = 'legacy',
    stats_status = 'applied'
WHERE price_pnl_usd IS NULL AND realized_usd IS NOT NULL;

-- skipped 行（realized_usd IS NULL）标记 legacy/skipped
UPDATE position_settlements
SET pnl_quality = 'legacy',
    completeness_status = 'skipped',
    stats_status = 'skipped'
WHERE price_pnl_usd IS NULL AND realized_usd IS NULL;

-- CHECK 约束（新行强制合法值，legacy 行已回填）
ALTER TABLE position_settlements ADD CONSTRAINT settlement_pnl_quality_check
    CHECK (pnl_quality IS NULL OR pnl_quality = ANY (ARRAY['actual','estimated','legacy','unknown']));
ALTER TABLE position_settlements ADD CONSTRAINT settlement_completeness_check
    CHECK (completeness_status IS NULL OR completeness_status = ANY (ARRAY['complete','skipped','unknown','failed','legacy']));
ALTER TABLE position_settlements ADD CONSTRAINT settlement_stats_status_check
    CHECK (stats_status IS NULL OR stats_status = ANY (ARRAY['pending','applied','retryable','skipped']));
