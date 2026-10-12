-- D-266 CNY 逆回购利率雷达：上证质押式回购利率快照表。
-- 每次 Poll 写 4 行（GC001/GC007/GC014/GC028），不做 upsert——保留日内逐笔快照（历史回看+基线建立）。
-- 日级聚合（开/收/高/低）由查询时聚合，不预计算。
-- 数据量估算：66 tick/天 × 4 品种 = 264 行/天 × ~250 交易日/年 ≈ 66K 行/年（轻量，无需分区）。
CREATE TABLE IF NOT EXISTS fin_repo_rates (
    id          BIGSERIAL PRIMARY KEY,
    code        TEXT NOT NULL,            -- "204001" / "204007" / "204014" / "204028"
    name        TEXT NOT NULL,            -- "GC001" / "GC007" / ...
    rate_pct    DOUBLE PRECISION NOT NULL, -- 年化利率%（= f2 最新价 / 1000）
    change_pct  DOUBLE PRECISION,         -- 涨跌幅%（f152，可空）
    ts          TIMESTAMPTZ NOT NULL DEFAULT now(), -- 采集时间
    trade_date  DATE NOT NULL             -- 交易日（北京时间）
);

CREATE INDEX IF NOT EXISTS idx_repo_rates_code_ts ON fin_repo_rates (code, ts DESC);
CREATE INDEX IF NOT EXISTS idx_repo_rates_trade_date ON fin_repo_rates (trade_date);
