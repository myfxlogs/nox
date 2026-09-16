CREATE TABLE fin_cex_universe (
  venue text NOT NULL, symbol text NOT NULL,
  base text NOT NULL, underlying_class text NOT NULL DEFAULT '',
  status text NOT NULL, launch_ts timestamptz,
  first_seen timestamptz NOT NULL DEFAULT now(),
  last_seen timestamptz NOT NULL DEFAULT now(),
  hedge_canonical text NOT NULL DEFAULT '',   -- join 命中的对冲腿 canonical，空=无腿
  tier text NOT NULL DEFAULT 'T3',            -- T1/T2/T3
  last_funding_ann numeric, last_vol24h numeric,
  last_alert_ts timestamptz,
  PRIMARY KEY (venue, symbol));
CREATE TABLE fin_cex_events (
  id bigserial PRIMARY KEY, ts timestamptz NOT NULL DEFAULT now(),
  venue text NOT NULL, symbol text NOT NULL, event_type text NOT NULL, detail text NOT NULL DEFAULT '');
CREATE INDEX idx_fin_cex_events_key ON fin_cex_events(event_type, symbol, ts DESC);
-- FUNDING_DIVERGENCE 行约定：venue='multi'、symbol=canonical、detail='okx:binance -12.3% vs +8.1% ann'
CREATE TABLE fin_cex_radar (
  ts timestamptz NOT NULL DEFAULT now(),
  venue text NOT NULL, symbol text NOT NULL,
  funding_ann numeric, vol24h numeric, mark numeric, idx numeric,
  hedge_canonical text NOT NULL DEFAULT '', tier text NOT NULL DEFAULT 'T3');
CREATE INDEX idx_fin_cex_radar_ts ON fin_cex_radar(ts);
