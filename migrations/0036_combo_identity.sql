-- 0036_combo_identity: D-080 组合键贯通——机会表组合键化 + 持仓承诺快照 + combo 日度 rollup
-- 设计 SSOT: docs/机会组合键贯通与验证闭环方案.md
--
-- 此 migration 包含 S2（机会表组合键化）+ S3（持仓承诺快照列）+ S4（rollup 表）的全部 DDL。
-- S3/S4 代码在后续 commit；migration 前向兼容（新列 nullable，新表空）。

-- === S2: 机会表组合键化 ===

-- 1. 归档旧表（保留历史持仓 oppID 解析 + rollup 回填源，不清理）
ALTER TABLE opportunities RENAME TO opportunities_tick;

-- 2. 新建 opportunities（同列，不用 INCLUDING CONSTRAINTS——会复制旧 PK；新表自行加）
CREATE TABLE opportunities (LIKE opportunities_tick INCLUDING DEFAULTS);
ALTER TABLE opportunities ADD COLUMN last_seen TIMESTAMPTZ NOT NULL DEFAULT now();
ALTER TABLE opportunities ADD PRIMARY KEY (id);

-- 3. 索引（旧索引随 RENAME 归属 opportunities_tick，新表需重建；名称加 _new 避免冲突）
CREATE INDEX idx_opportunities_expires_new ON opportunities (expires_at DESC);
CREATE INDEX idx_opportunities_type_quote_new ON opportunities (type, quote_time DESC);

-- 4. 初始灌入：每组合最新一行（DISTINCT ON 表达式 = ORDER BY 首表达式）
--    id = 组合键（前 6 段），非旧 tick ID（7 段）
INSERT INTO opportunities (id, type, canonical, venue_buy, venue_sell, price_buy, price_sell,
  gross_bps, spread_cost, commission_cost, slippage_cost, swap_cost, net_bps, breakeven_days,
  quote_time, expires_at, last_seen)
SELECT DISTINCT ON (array_to_string((string_to_array(id,':'))[1:6],':'))
  array_to_string((string_to_array(id,':'))[1:6],':') AS combo_key,
  type, canonical, venue_buy, venue_sell, price_buy, price_sell,
  gross_bps, spread_cost, commission_cost, slippage_cost, swap_cost, net_bps, breakeven_days,
  quote_time, expires_at, now()
FROM opportunities_tick
ORDER BY array_to_string((string_to_array(id,':'))[1:6],':'), quote_time DESC;

-- === S3: 持仓承诺快照 ===

-- 5. positions 加 expected_annual_pct（按型换算见方案 §4）
ALTER TABLE positions ADD COLUMN expected_annual_pct NUMERIC(10,4);

-- 6. 历史回填：promised=开仓时点快照（LATERAL join 取 opened_at 前最近 tick 行）
--    口径：该持仓 opened_at 前最近的 tick 行 net_bps → 按型换算
--    opened_at IS NULL 的持仓回退组合最新值（DISTINCT ON 组合键 DESC quote_time）
--    按型换算：FUNDING 直存 net_bps / SWAPARB·CARRY=net_bps×365/100 / 收敛型 NULL
UPDATE positions p SET expected_annual_pct = sub.annual_pct
FROM (
  SELECT
    p2.id AS pos_id,
    CASE
      WHEN t.type IN ('FUNDING_HEDGE','PERP_FUNDING') THEN t.net_bps
      WHEN t.type IN ('SWAPARB','CARRY') THEN t.net_bps * 365 / 100
      ELSE NULL
    END AS annual_pct
  FROM positions p2
  LEFT JOIN LATERAL (
    SELECT type, net_bps
    FROM opportunities_tick ot
    WHERE array_to_string((string_to_array(ot.id,':'))[1:6],':')
        = array_to_string((string_to_array(p2.opportunity_id,':'))[1:6],':')
      AND ot.quote_time <= p2.opened_at
    ORDER BY ot.quote_time DESC
    LIMIT 1
  ) t ON true
  WHERE p2.expected_annual_pct IS NULL
) sub
WHERE p.id = sub.pos_id;

-- 6b. opened_at IS NULL 的持仓回退组合最新值（DISTINCT ON 组合键 DESC quote_time）
UPDATE positions p SET expected_annual_pct = sub.annual_pct
FROM (
  SELECT DISTINCT ON (array_to_string((string_to_array(id,':'))[1:6],':'))
    array_to_string((string_to_array(id,':'))[1:6],':') AS combo_key,
    type, net_bps,
    CASE
      WHEN type IN ('FUNDING_HEDGE','PERP_FUNDING') THEN net_bps
      WHEN type IN ('SWAPARB','CARRY') THEN net_bps * 365 / 100
      ELSE NULL
    END AS annual_pct
  FROM opportunities_tick
  ORDER BY array_to_string((string_to_array(id,':'))[1:6],':'), quote_time DESC
) sub
WHERE array_to_string((string_to_array(p.opportunity_id,':'))[1:6],':') = sub.combo_key
  AND p.expected_annual_pct IS NULL
  AND p.opened_at IS NULL;

-- === S4: combo 日度 rollup ===

-- 7. rollup 表（write-behind 累积，PK=combo+day）
CREATE TABLE combo_daily_rollup (
  combo_key  TEXT NOT NULL,
  day        DATE NOT NULL,
  type       TEXT NOT NULL,
  samples    INT NOT NULL DEFAULT 0,
  net_avg    NUMERIC(20,8) NOT NULL DEFAULT 0,
  net_min    NUMERIC(20,8) NOT NULL DEFAULT 0,
  net_max    NUMERIC(20,8) NOT NULL DEFAULT 0,
  annual_avg NUMERIC(10,4),
  first_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (combo_key, day)
);

-- 8. rollup 回填：opportunities_tick GROUP BY 组合/日/型
--    覆盖度=过门槛子集（D-078 门槛在先，tick 表只收过门槛组合）；
--    「全组合含低于门槛」的完整覆盖自本方案部署起。
INSERT INTO combo_daily_rollup (combo_key, day, type, samples, net_avg, net_min, net_max, annual_avg, first_at, last_at)
SELECT
  array_to_string((string_to_array(id,':'))[1:6],':') AS combo_key,
  (quote_time AT TIME ZONE 'UTC')::date AS day,
  type,
  COUNT(*)::int AS samples,
  AVG(net_bps) AS net_avg,
  MIN(net_bps) AS net_min,
  MAX(net_bps) AS net_max,
  CASE WHEN type IN ('FUNDING_HEDGE','PERP_FUNDING') THEN AVG(net_bps) ELSE NULL END AS annual_avg,
  MIN(quote_time) AS first_at,
  MAX(quote_time) AS last_at
FROM opportunities_tick
GROUP BY 1, 2, 3
ON CONFLICT (combo_key, day) DO NOTHING;
