-- 0126_margin_guard.sql - D-405 S1：账户资金三档监控（保证金安全网）数据面。
-- ① fin_venue_account_snapshot 补 buffer_multiple：CEX「距强平缓冲倍数」=equity/MMR，
--    写时归一（binance=marginBalance/maintMargin；okx=mgnRatio/100）——两源原生指标
--    方向相反，列名编码语义防二次方向误用；nullable 后加零回填（未采=NULL 诚实缺省）。
-- ② fin_venue_margin_status：marginGuard 每次评估 upsert 的判定结果小表——
--    RPC ListAccountMarginStatus 数据源 + last_alert_at 落库使分档冷却跨重启可恢复。
ALTER TABLE fin_venue_account_snapshot ADD COLUMN IF NOT EXISTS buffer_multiple double precision;

CREATE TABLE fin_venue_margin_status (
  venue                text NOT NULL,
  venue_type           text NOT NULL DEFAULT '',
  tier                 text NOT NULL,             -- normal / warn / urgent / data_stale
  coverage_days        double precision,          -- 余粮覆盖天数（NULL=不可算）
  daily_vol_usd        double precision,          -- Σ(OPEN 腿名义额×日波动) USD
  suggested_topup_usd  double precision,          -- 预警档缺口额（够档=0）
  last_alert_at        timestamptz,               -- 冷却锚（重启安全）
  updated_at           timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (venue, venue_type)
);
