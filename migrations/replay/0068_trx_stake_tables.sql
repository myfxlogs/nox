-- 0068_trx_stake_tables.sql: TRX 质押监控 P1（D-211，SSOT §3）
-- 3 新表（fin_trx_account/fin_trx_txs/fin_trx_witness）+ position_daily_income 加 source 列。
-- 纯只读采集：TronGrid API → fin_trx_* 表 → daily rollup → position_daily_income(source='auto')。

-- 3.1 采集原始层：账户快照（5min 级）
CREATE TABLE fin_trx_account (
    id              BIGSERIAL PRIMARY KEY,
    address         VARCHAR(64) NOT NULL,
    votes_total     BIGINT NOT NULL,            -- tronPowerLimit
    sr_address      VARCHAR(64),                 -- votes[0].vote_address
    balance_sun     BIGINT NOT NULL,             -- 账户 TRX 余额（进出账对账基准）
    allowance_sun   BIGINT NOT NULL,             -- 投票奖励累积（1 sun=1e-6 TRX）
    energy_delegated BIGINT NOT NULL,            -- 委托出的能量质押（sun）
    bandwidth_staked BIGINT NOT NULL,            -- 自押带宽（sun）
    net_limit       BIGINT NOT NULL,
    unfrozen        JSONB,                        -- 解押在途快照（可空，无在途=NULL）
    fetched_at      TIMESTAMPTZ NOT NULL DEFAULT now()  -- 量级：5min 快照 ≈10.5 万行/年
);

CREATE INDEX fin_trx_account_fetched_idx ON fin_trx_account (fetched_at);

-- 3.2 收入交易流（幂等键 tx_id）
CREATE TABLE fin_trx_txs (
    id           BIGSERIAL PRIMARY KEY,
    tx_id        VARCHAR(64) UNIQUE NOT NULL,
    ts           TIMESTAMPTZ NOT NULL,           -- block_timestamp
    kind         VARCHAR(16) NOT NULL,           -- rental_income|vote_claim|deposit|withdraw|stake|unstake|other
    amount_sun   BIGINT NOT NULL,
    counterparty VARCHAR(64),
    fetched_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX fin_trx_txs_ts_idx ON fin_trx_txs (ts DESC);

-- 3.3 见证人快照（慢变，24h）
CREATE TABLE fin_trx_witness (
    id          BIGSERIAL PRIMARY KEY,
    sr_address  VARCHAR(64) NOT NULL,
    url         VARCHAR(256),                    -- listwitnesses 实有字段（name/brokerage 无 TronGrid 源，删列）
    vote_count  BIGINT,
    fetched_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 3.4 手工/自动记账共存（D-208 NULL 教训：NOT NULL DEFAULT 避免 NULL 扫描）
ALTER TABLE position_daily_income ADD COLUMN IF NOT EXISTS source VARCHAR(8) NOT NULL DEFAULT 'manual';
