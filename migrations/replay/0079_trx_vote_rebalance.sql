-- D-269 TRX 投票收益优化器：决策记录表。
-- 每次 6h tick 决策落库（无论是否执行），执行后回填 tx_hash/status。
-- 数据量估算：1 行/6h × 4 次/天 × 365 天 ≈ 1.5K 行/年（轻量，无需分区）。
CREATE TABLE IF NOT EXISTS fin_trx_vote_rebalance (
    id              SERIAL PRIMARY KEY,
    decided_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    current_yield   DOUBLE PRECISION NOT NULL,      -- 当前预期年化 TRX
    target_yield    DOUBLE PRECISION NOT NULL,      -- 目标预期年化 TRX
    improvement_pct DOUBLE PRECISION NOT NULL,      -- 改善幅度 %
    need_rebalance  BOOLEAN NOT NULL,
    reason          TEXT NOT NULL,
    target_votes    JSONB NOT NULL,                 -- [{"address":"T...","votes":5000},...]
    executed_at     TIMESTAMPTZ,
    tx_hash         TEXT,
    status          TEXT NOT NULL DEFAULT 'pending', -- pending/executed/failed/skipped
    fail_reason     TEXT
);

CREATE INDEX IF NOT EXISTS idx_trx_vote_rebalance_decided_at ON fin_trx_vote_rebalance(decided_at);
CREATE INDEX IF NOT EXISTS idx_trx_vote_rebalance_status ON fin_trx_vote_rebalance(status);
