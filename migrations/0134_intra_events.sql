-- 0134_intra_events.sql —— D-441 S2：日内纸面轨栅栏事件表（kill/circuit 落库，裁决统计数据源）。
-- kind 只允许 kill/circuit；stop/eod/open 属正常交易行为不落事件表（噪音纪律）。
CREATE TABLE IF NOT EXISTS intra_events (
  id        BIGSERIAL PRIMARY KEY,
  ts        TIMESTAMPTZ NOT NULL DEFAULT now(),
  kind      TEXT NOT NULL CHECK (kind IN ('kill','circuit')),
  baseline  TEXT NOT NULL DEFAULT '',
  detail    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_intra_events_kind ON intra_events (kind, ts);
