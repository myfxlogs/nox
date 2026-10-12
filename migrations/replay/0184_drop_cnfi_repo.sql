-- D-139：CNFI/逆回购面整体摘除——13 表幂等 DROP。
-- 生产已备份：/opt/arb-backups/d139_cnfi_repo_20261009_022520.sql.gz（SG 15433）。
-- IF EXISTS 幂等契约：可重放；历史建表迁移（0070/0073-0075/0080/0082/0085/0087/0078 fin_repo_rates）不改写。
-- 顺序：引用方先删（cnfi_nav/cnfi_series.code REFERENCES cnfi_instruments），被引用表殿后。
DROP TABLE IF EXISTS cnfi_actions;
DROP TABLE IF EXISTS cnfi_calendar;
DROP TABLE IF EXISTS cnfi_holdings;
DROP TABLE IF EXISTS cnfi_nav;
DROP TABLE IF EXISTS cnfi_probe_runs;
DROP TABLE IF EXISTS cnfi_repo_config;
DROP TABLE IF EXISTS cnfi_repo_ops;
DROP TABLE IF EXISTS cnfi_repo_trades;
DROP TABLE IF EXISTS cnfi_signal_config;
DROP TABLE IF EXISTS cnfi_signal_state;
DROP TABLE IF EXISTS cnfi_series;
DROP TABLE IF EXISTS fin_repo_rates;
DROP TABLE IF EXISTS cnfi_instruments;
