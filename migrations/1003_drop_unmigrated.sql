-- M3 前置①：不搬表族 DROP（搬迁清单 §3 裁定 84 搬/12 不搬）。
-- 切流窗口内先 `pg_dump -Fc` 归档至 SG `/opt/nox-backups/d212-archive/` 再应用本迁移；
-- IF EXISTS 幂等——鲜装库（baseline 无此族）重放零副作用。
DROP TABLE IF EXISTS fin_cmc_prices;
DROP TABLE IF EXISTS fin_cmc_positions;
DROP TABLE IF EXISTS fin_cmc_account_summary;
DROP TABLE IF EXISTS fin_cmc_history;
DROP TABLE IF EXISTS capital_accounts;
DROP TABLE IF EXISTS capital_account_venue_links;
DROP TABLE IF EXISTS capital_decisions;
DROP TABLE IF EXISTS hypotheses;
DROP TABLE IF EXISTS price_alerts;
DROP TABLE IF EXISTS investment_records;
DROP TABLE IF EXISTS paper_intra_trades;
DROP TABLE IF EXISTS intra_events;
