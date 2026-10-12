-- 0071_trx_vote_apr.sql: TRX 投票收益率监控（D-231，SSOT §S1/S4）
-- 新表 fin_trx_vote_apr：每轮聚合一行（288 行/日，5min 节流），拒绝全 SR 快照洪水。
-- current_rate=票数加权平均年化（S1.2 探针核真口径）；max_rate=producer=true 最高年化。
-- 纯只读采集：TRONSCAN /api/witness + /api/vote → 聚合 → fin_trx_vote_apr → 卡片展示。

CREATE TABLE fin_trx_vote_apr (
    id            BIGSERIAL PRIMARY KEY,
    fetched_at    TIMESTAMPTZ NOT NULL DEFAULT now(),  -- 量级：5min 聚合 ≈10.5 万行/年
    current_rate  DOUBLE PRECISION NOT NULL DEFAULT 0, -- 当前投票票数加权平均年化 %
    current_sr    VARCHAR(64),                          -- 当前投票主 SR 名（票数最多者）
    max_rate      DOUBLE PRECISION NOT NULL DEFAULT 0,  -- 市场最高年化 %（producer=true 降序第一）
    max_sr        VARCHAR(64),                          -- 最高年化 SR 名
    spread        DOUBLE PRECISION NOT NULL DEFAULT 0   -- max_rate − current_rate（pp）
);

CREATE INDEX fin_trx_vote_apr_fetched_idx ON fin_trx_vote_apr (fetched_at);
