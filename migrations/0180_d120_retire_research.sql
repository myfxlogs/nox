-- D-120 研究面 A 批清退：13 表幂等 DROP（生产已 pg_dump 备份至 /opt/arb-backups/d120_dropped_tables_20261008_015354.sql.gz）。
-- IF EXISTS 幂等：重复执行无副作用；CASCADE 兜底残留依赖。
DROP TABLE IF EXISTS calib_params CASCADE;
DROP TABLE IF EXISTS calib_scoreboard CASCADE;
DROP TABLE IF EXISTS capital_account_venue_links CASCADE;
DROP TABLE IF EXISTS capital_accounts CASCADE;
DROP TABLE IF EXISTS capital_decisions CASCADE;
DROP TABLE IF EXISTS capital_snapshots CASCADE;
DROP TABLE IF EXISTS cot_position CASCADE;
DROP TABLE IF EXISTS event_cases CASCADE;
DROP TABLE IF EXISTS fred_observation CASCADE;
DROP TABLE IF EXISTS hypotheses CASCADE;
DROP TABLE IF EXISTS investment_records CASCADE;
DROP TABLE IF EXISTS ledger CASCADE;
DROP TABLE IF EXISTS paper_trades CASCADE;
