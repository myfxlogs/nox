-- 0164 D-006 S3：canonical 聚簇敞口闸表（schema 镜像 fin_venue_exposure_caps）。
-- 语义：查无行→默认 6000（Go 侧 DefaultCanonicalCapUSD）；enabled=false→闸停用；
-- enabled+cap_usd=0→冻结新入场。修 XAUUSD 类同标的双仓同向基差聚簇风险。

CREATE TABLE IF NOT EXISTS fin_canonical_exposure_caps (
    canonical   TEXT PRIMARY KEY,                          -- 标的 canonical（XAUUSD/BTCUSDT…）
    cap_usd     NUMERIC NOT NULL CHECK (cap_usd >= 0),     -- 0=显式冻结新入场
    enabled     BOOLEAN NOT NULL DEFAULT true,             -- false=停用该闸（行仍在档）
    note        TEXT NOT NULL DEFAULT '',
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- D-006 S3 TOCTOU：守卫建库事务内写入的 canonical 归属投影——聚簇复查须看见
-- 同窗兄弟仓（其腿行在建库事务提交后才由 persistOpenLegs 落地，纯腿归属不可见）。
CREATE TABLE IF NOT EXISTS fin_position_canonicals (
    position_id BIGINT NOT NULL REFERENCES positions(id) ON DELETE CASCADE,
    canonical   TEXT NOT NULL,
    PRIMARY KEY (position_id, canonical)
);
