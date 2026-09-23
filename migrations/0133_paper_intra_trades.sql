-- 0133_paper_intra_trades.sql —— D-425 S1：日内单品高风险轨 v1（纸面·硬风险栅栏）。
-- 独立沙箱轨（业主 9-23 令）：与主线资本隔离+代码隔离（新 engine 文件族），真金挂 D-158
-- 业主域三条件门，v1 仅纸面。全观测：信号/进出场/止损触发/滑点/熔断全字段（首月周报复盘死因分布）。
CREATE TABLE IF NOT EXISTS paper_intra_trades (
  id            BIGSERIAL PRIMARY KEY,
  baseline      TEXT NOT NULL CHECK (baseline IN ('momentum_break','meanrev')),
  symbol        TEXT NOT NULL,
  venue         TEXT NOT NULL,
  side          TEXT NOT NULL CHECK (side IN ('long','short')),
  status        TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','closed')),
  entry_ts      TIMESTAMPTZ NOT NULL,
  entry_px      DOUBLE PRECISION NOT NULL,
  qty           DOUBLE PRECISION NOT NULL,
  notional_usd  DOUBLE PRECISION NOT NULL,
  margin_usd    DOUBLE PRECISION NOT NULL,
  stop_px       DOUBLE PRECISION NOT NULL,
  exit_ts       TIMESTAMPTZ,
  exit_px       DOUBLE PRECISION,
  exit_reason   TEXT NOT NULL DEFAULT '',
  slippage_bps  DOUBLE PRECISION,
  pnl_usd       DOUBLE PRECISION,
  signal_note   TEXT NOT NULL DEFAULT '',
  trade_date    DATE NOT NULL, -- 插入端由 entry_ts::date 计算写入（PG 禁 DEFAULT 引用列，0A000）
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_intra_status ON paper_intra_trades (status);
CREATE INDEX IF NOT EXISTS idx_intra_date ON paper_intra_trades (trade_date, baseline);
