-- 0178_drop_perp_funding_admission.sql D-057⑤：PERP_FUNDING 证据准入表退役——
-- 族停用（准入面整族删除：评估器/RPC/面板/表），87 行已 pg_dump 备份至
-- /opt/arb-backups/perp_funding_admission-2026-10-04.sql.gz。
-- IF EXISTS 幂等可重放。
DROP TABLE IF EXISTS perp_funding_admission;
