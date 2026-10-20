-- 1060_ticks_key_ts_idx.sql —— 现态历史分层方案 §4 P1 止血：
-- ticks 复合索引 (venue,symbol,venue_type,ts DESC)——LatestTick/LatestTickMid
-- 倒扫整 chunk（262 万行/322MB=7.5s 单条烧 CPU）病灶止血。
-- hypertable/分区父表均不支持 CONCURRENTLY——单语句普通建索引自动级联子表
-- （一次性写停窗=数十秒，采样器节流语义天然容忍）。
CREATE INDEX IF NOT EXISTS ticks_key_ts_idx ON ticks (venue, symbol, venue_type, ts DESC);
