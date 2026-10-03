-- 0174_drop_opportunities_tick.sql D-040②：第一波精简——
-- opportunities_tick（机会 tick 快照，旧 D-036 组合身份前产物）
-- 40,129 行·stale 2026-08-22·Go 代码零引用（D-039 盘点实证）。
-- 删除前已 pg_dump 备份至 /opt/arb-backups/opportunities_tick-<date>.sql.gz。
-- IF EXISTS 幂等可重放。
DROP TABLE IF EXISTS opportunities_tick;
