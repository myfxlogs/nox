-- 0121_venue_account_snapshot_src.sql - D-383 B1/B2：快照行写路标记 src 列。
-- 事件行写 mt_event（MT order-event）/okx_event（OKX private WS push），调度回放行
-- 走列默认 scheduler——幂等：存量行/回放行自动归 scheduler，不需回填。
ALTER TABLE fin_venue_account_snapshot ADD COLUMN IF NOT EXISTS src TEXT NOT NULL DEFAULT 'scheduler';
