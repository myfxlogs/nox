-- D-121：facts NULL-symbol 行（kind,venue）枚举快路（LatestFactsByKindSince 跳扫描的 NULL 组补面）。
-- 部分索引只收 symbol IS NULL 行（生产实拍 3.3 万/503 万行），体积可忽。
-- 号位注记：本票原占 0180 与 D-120（d120_retire_research）撞号；D-120 先合占位，本票复审顺延 0181。
CREATE INDEX IF NOT EXISTS facts_null_symbol_kind_venue_ts_idx
    ON facts (kind, venue, ts DESC) WHERE symbol IS NULL;
