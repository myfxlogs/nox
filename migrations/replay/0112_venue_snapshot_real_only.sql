-- 0112_venue_snapshot_real_only.sql - D-360 S3：fin_venue_account_snapshot 只留 real 账户（幂等可重跑）。
-- 业主令：覆盖率面板只收 real 账户，demo 进来反是噪声（实证：MT ×7 demo 混排/gate 纯 sim/
-- binance perp sim 撞键恒赢——SSOT §0 表）。
-- ① MT demo：venue 不在 broker_accounts.is_real 集合（NOT EXISTS 写法避免 NOT IN+NULL 静默失效；
--   核证点②：s.venue ≡ b.user_id::text，生产 DB 两列互证留证 9-20）。
DELETE FROM fin_venue_account_snapshot s
WHERE s.venue_type IN ('mt4','mt5')
  AND NOT EXISTS (SELECT 1 FROM broker_accounts b
                  WHERE b.user_id::text = s.venue AND b.is_real);
-- ② CEX sim 源行：该 venue+type 存在 simulated 配置者其历史行全为 sim 源
--    （gate=纯 sim；binance/perp=sim 撞键恒赢已实证 4533.60=sim 权益）——全删，
--    未来 monitor 撞键修复后真盘新行自会落库。
DELETE FROM fin_venue_account_snapshot s
WHERE s.venue_type NOT IN ('mt4','mt5')
  AND EXISTS (SELECT 1 FROM exchange_configs e
              WHERE e.venue = s.venue AND e.venue_type = s.venue_type AND e.simulated);
