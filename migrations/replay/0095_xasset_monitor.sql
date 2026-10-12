-- D-299 PAXG-XAU 跨市场套利监控快照表
CREATE TABLE fin_xasset_monitor (
  id bigserial PRIMARY KEY,
  ts timestamptz NOT NULL DEFAULT now(),
  pair text NOT NULL DEFAULT 'PAXG-XAU',
  binance_bid numeric(20,8) NOT NULL,
  binance_ask numeric(20,8) NOT NULL,
  exness_bid numeric(20,8) NOT NULL,
  exness_ask numeric(20,8) NOT NULL,
  entry_spread_bps numeric(20,8) NOT NULL,
  exit_spread_bps numeric(20,8) NOT NULL,
  entry_net_bps numeric(20,8) NOT NULL,
  funding_rate_8h numeric(20,12) NOT NULL,
  funding_annual_pct numeric(10,4) NOT NULL,
  tracking_error_bps numeric(20,8) NOT NULL,
  equiv_annual_pct numeric(10,4) NOT NULL
);
CREATE INDEX idx_xasset_monitor_ts ON fin_xasset_monitor(ts DESC);
CREATE INDEX idx_xasset_monitor_pair_ts ON fin_xasset_monitor(pair, ts DESC);
