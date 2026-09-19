-- D-336 S2：carry 宇宙扫描器快照表（发现层根修——facts 双面联合零联合消费的根因闭环）。
-- 每轮 upsert 当轮全行（当前快照语义）；历史靠 ts 滚动保留 30d（复用 facts 保留策略模式）。
CREATE TABLE IF NOT EXISTS fin_carry_universe (
    id                BIGSERIAL PRIMARY KEY,
    ts                TIMESTAMPTZ NOT NULL DEFAULT now(),
    canonical         TEXT NOT NULL,
    mt_venue          TEXT NOT NULL,
    cex_venue         TEXT NOT NULL,
    net_annual_pct    NUMERIC NOT NULL,
    funding_annual_pct NUMERIC NOT NULL,
    swap_long_pct     NUMERIC,
    swap_short_pct    NUMERIC,
    swap_free         BOOLEAN NOT NULL DEFAULT false,
    structure         TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS fin_carry_universe_ts_idx ON fin_carry_universe (ts DESC);
CREATE INDEX IF NOT EXISTS fin_carry_universe_net_idx ON fin_carry_universe (net_annual_pct DESC);
