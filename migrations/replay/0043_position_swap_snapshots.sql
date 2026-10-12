-- D-110 P2: position swap 自动采集快照表
-- broker_position_monitor 每次 poll 后，swap 值变化时写入累计值快照。
-- 对账模块 swapledger/reconcile_swap.go 用今日末条 - 昨日末条算当日实际增量。
CREATE TABLE position_swap_snapshots (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    position_id BIGINT NOT NULL REFERENCES positions(id) ON DELETE CASCADE,
    venue       TEXT NOT NULL,
    swap_usd    DOUBLE PRECISION NOT NULL,
    ts          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX position_swap_snapshots_pos_ts_idx ON position_swap_snapshots (position_id, ts DESC);
