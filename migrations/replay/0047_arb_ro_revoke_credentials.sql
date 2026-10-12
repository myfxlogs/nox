-- 0047_arb_ro_revoke_credentials.sql - D-161 v2 补偿④：arb_ro 列级收权（防分析员 DSN 读凭据明文）
-- 0041 授予 public 全表 SELECT → arb_ro 可读 broker_accounts.password / exchange_configs.api_key 等明文。
-- 本 migration 收回全表 SELECT，改为列级授权（排除凭据列）。
-- 幂等：REVOKE + GRANT 可重复执行。

-- broker_accounts：收回全表 SELECT，授予非凭据列
REVOKE SELECT ON TABLE broker_accounts FROM arb_ro;
GRANT SELECT (
    id, venue, platform, host, port, user_id, server, alias, status, created_at, commission_bps, is_real, website
) ON broker_accounts TO arb_ro;

-- exchange_configs：收回全表 SELECT，授予非凭据列
REVOKE SELECT ON TABLE exchange_configs FROM arb_ro;
GRANT SELECT (
    id, venue, venue_type, label, base_url, created_at, simulated, website
) ON exchange_configs TO arb_ro;
