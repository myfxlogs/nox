-- 0136_cex_private_events.sql —— D-450 S4：CEX 私有 WS 事件审计表（gate/binance 用户流原文落库，
-- 审计+回放源；DEBUG 量级无 retention，后期可清）。event_key=去重/关联键（gate order id / binance i/c 等，
-- 取不到时存事件类型串；不建 UNIQUE——WS 重发属正常，回放面须原样保留）。
CREATE TABLE IF NOT EXISTS cex_private_events (
  id          BIGSERIAL PRIMARY KEY,
  venue       TEXT NOT NULL,
  venue_type  TEXT NOT NULL,
  channel     TEXT NOT NULL,
  event_key   TEXT NOT NULL DEFAULT '',
  payload     JSONB NOT NULL,
  received_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_cex_private_events_vcr ON cex_private_events (venue, channel, received_at DESC);
