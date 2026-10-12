-- 0113_okx_unified_cleanup.sql - D-361 S3：OKX 统一账户旧双行清场（幂等）。
-- 写侧归一后新行为 venue_type='unified'（okx 统一账户 spot/perp 双 config 同钱，
-- 旧 spot/perp 双行=双计）；本迁移删旧双行，unified 新行自落。
DELETE FROM fin_venue_account_snapshot WHERE venue = 'okx' AND venue_type IN ('spot', 'perp');
