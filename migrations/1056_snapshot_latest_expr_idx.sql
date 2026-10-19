-- 1056：LatestVenueAccountSnapshots LATERAL 探针索引——key_ts_idx 在案但
-- `currency IS NOT DISTINCT FROM` 不被 planner 当等值索引用（改走 venue-only
-- 索引探 118 键×滤 1.6K 行=2.2s 反劣）。表达式索引 (venue,venue_type,
-- COALESCE(currency,''),fetched_at DESC) 让 COALESCE 等值直上索引探针。
CREATE INDEX IF NOT EXISTS fin_venue_snapshot_latest_expr_idx
  ON fin_venue_account_snapshot (venue, venue_type, COALESCE(currency,''), fetched_at DESC);
