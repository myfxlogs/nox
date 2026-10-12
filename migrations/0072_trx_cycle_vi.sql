-- 0072_trx_cycle_vi.sql: TRX 待领取实时化（D-233，SSOT §S3）
-- 新表 fin_trx_cycle_vi：Vi（票权时间积分）累积快照，upsert by (cycle, sr_address)。
-- Vi = Σ deltaVi = Σ (reward_cycle × 10^18 / voteCount)，严格复刻 java-tron DelegationStore.accumulateWitnessVi。
-- 待领取 = allowance + Σ_SRs ΔVi × userVote ÷ 10^18（复刻 MortgageService.queryReward）。
-- cycle=自有步进号（allowance 步进检测触发+1，不依赖链上内部周期号）；user_votes=用户对该 SR 的票数（待领取计算用）。
-- 纯只读采集：TRONSCAN /api/witness annualizedRate → 数值积分 Vi → fin_trx_cycle_vi → GetHedgeStructure 现算待领取。

CREATE TABLE fin_trx_cycle_vi (
    id          BIGSERIAL PRIMARY KEY,
    cycle       BIGINT NOT NULL,                  -- 自有步进号（allowance 步进+1，非链上 cycle 号）
    sr_address  VARCHAR(64) NOT NULL,             -- SR 地址
    vi          NUMERIC NOT NULL DEFAULT 0,       -- BigInteger（10^18 scale，java-tron DECIMAL_OF_VI_REWARD=10^18）
    user_votes  BIGINT NOT NULL DEFAULT 0,        -- 用户对该 SR 的票数（待领取=Σ vi×user_votes/10^18 用）
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (cycle, sr_address)
);

CREATE INDEX fin_trx_cycle_vi_cycle_idx ON fin_trx_cycle_vi (cycle DESC, sr_address);
