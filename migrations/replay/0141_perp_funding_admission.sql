-- 0141 D-491 S1：PERP_FUNDING 证据准入表——硬编码白名单（BTC/ETH/TRX）退役，
-- 由评估器按 7d 滚窗判据自动定 status；owner_override 业主钉死优先于评估；
-- pending_depth 行进评估器 depth_want 集反向扩采深度证据（闭环无人值守）。

CREATE TABLE IF NOT EXISTS perp_funding_admission (
    canonical        text PRIMARY KEY,
    status           text NOT NULL DEFAULT 'denied',  -- admitted/pending_depth/review/denied（评估器裁决）
    best_pair        text NOT NULL DEFAULT '',        -- 'binance↔okx'（评估器判定最优 real-real 对）
    mean_spread      double precision NOT NULL DEFAULT 0,  -- 最优对 7d 均值差（年化百分点）
    spread_sd        double precision NOT NULL DEFAULT 0,  -- 最优对 pair 差 σ
    dir_consistency  double precision NOT NULL DEFAULT 0,  -- 0..1 同向占比（同一 venue 持续高侧）
    depth_min_usd    double precision,                     -- 两腿 mid_depth_usd 较小者；NULL=未验
    samples          int NOT NULL DEFAULT 0,               -- 最优对两腿窗内样本数较小值
    owner_override   text NOT NULL DEFAULT '',             -- ''/pinned/denied——业主钉死优先于评估（只由 RPC 写）
    note             text NOT NULL DEFAULT '',
    evaluated_at     timestamptz,
    updated_at       timestamptz NOT NULL DEFAULT now()
);
