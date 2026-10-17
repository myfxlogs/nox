-- 1044_combo_kill_trigger.sql —— kill 扳机判据列（D-599 续：淘汰数学化——
-- 判据落表非硬编码；NULL=该组合 kill 闸不开）。
-- kill_net_pnl_usd：累计净 PnL 触发下限（负值阈值——低于即摘）。
-- kill_loss_streak：连败扳机（连续净亏结算次数 ≥N 即摘）。
ALTER TABLE xasset_allow_combos
  ADD COLUMN IF NOT EXISTS kill_net_pnl_usd numeric,
  ADD COLUMN IF NOT EXISTS kill_loss_streak integer
    CHECK (kill_loss_streak IS NULL OR kill_loss_streak > 0);
