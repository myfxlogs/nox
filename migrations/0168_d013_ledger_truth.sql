-- 0168_d013_ledger_truth.sql —— D-013 账本单真相。
-- (a) completeness_status CHECK 增 'partial'（EXTERNAL 平仓腿 fee_usd NULL 语义）。
-- (b) #497–#507 全终态腿补 leg:<id> commission 现金流行——CEX=−fee_usd 实采、MT 显式 0。
-- (c) exit_kind 权威回填：legged_unwind（从未双腿成交/OPEN FAILED 腿）
--     > incident_close（await_session 或 >30min 且窗口内有残腿告警）> other（NULL 兜底）。
-- (d) realized_pnl / net_realized_usd 按 D-013 单源式重算：
--     net = Σ腿价差 + Σ(funding,swap) + Σ(leg: 键 commission)；旧 exec:* 键 commission 行
--     腿键行铺位后退出计算（pgstore.PositionNetUSD 同规则），留档不删。
-- (e) 事故族 incident_attributed=true 对齐（exit_kind 两事故值）。

-- (a) completeness 'partial' 合法化
ALTER TABLE position_settlements DROP CONSTRAINT settlement_completeness_check;
ALTER TABLE position_settlements ADD CONSTRAINT settlement_completeness_check
    CHECK (completeness_status IS NULL OR completeness_status = ANY (ARRAY['complete','partial','skipped','unknown','failed','legacy']));

-- (b) 腿键 commission 行回填（幂等：event_key ON CONFLICT DO NOTHING）
INSERT INTO position_cashflows
    (position_id, leg_no, kind, venue, venue_type, amount_usd, ts,
     account_id, event_key, source, quality, occurred_at, received_ts)
SELECT l.position_id, l.leg_no, 'commission', l.venue, l.venue_type,
       CASE WHEN l.venue_type IN ('mt4','mt5') THEN 0 ELSE -COALESCE(l.fee_usd, 0) END,
       COALESCE(l.ts, now()),
       CASE WHEN l.venue ~ '^\d+$' THEN l.venue::bigint END,
       'leg:' || l.id, 'exec', 'actual', COALESCE(l.ts, now()), now()
FROM position_legs l
WHERE l.position_id BETWEEN 497 AND 507
  AND l.status IN ('FILLED','EXTERNAL')
ON CONFLICT (event_key) WHERE event_key IS NOT NULL DO NOTHING;

-- (c) exit_kind 权威回填（次序即优先级）
UPDATE positions SET exit_kind = 'legged_unwind'
WHERE id BETWEEN 497 AND 507
  AND (status = 'FAILED'
       OR EXISTS (SELECT 1 FROM position_legs l
                  WHERE l.position_id = positions.id AND l.phase = 'OPEN' AND l.status = 'FAILED'));

UPDATE positions SET exit_kind = 'incident_close'
WHERE id BETWEEN 497 AND 507
  AND COALESCE(exit_kind, '') <> 'legged_unwind'
  AND (note LIKE '%await_session:%'
       OR (closed_at - closing_started_at > interval '30 minutes'
           AND EXISTS (SELECT 1 FROM alerts a
                       WHERE a.position_id = positions.id AND a.level = 'critical'
                         AND a.ts BETWEEN positions.closing_started_at AND positions.closed_at)));

UPDATE positions SET exit_kind = 'other'
WHERE id BETWEEN 497 AND 507 AND status = 'CLOSED' AND exit_kind IS NULL;

-- (e) 事故族 incident_attributed 对齐
UPDATE position_settlements s SET incident_attributed = true
FROM positions p
WHERE p.id = s.position_id AND p.id BETWEEN 497 AND 507
  AND p.exit_kind IN ('legged_unwind','incident_close');

-- (d) 净额重算（CLOSED 仓）
WITH price AS (
    SELECT o.position_id,
           SUM((c.price_actual - o.price_actual)
               * COALESCE(NULLIF(c.filled_qty,0), NULLIF(o.filled_qty,0), o.qty)
               * COALESCE(bsm.contract_size, 1)
               * CASE WHEN o.side = 'SELL' THEN -1 ELSE 1 END) AS pnl
    FROM position_legs o
    JOIN position_legs c
      ON c.position_id = o.position_id AND c.leg_no = o.leg_no
     AND c.phase = 'CLOSE' AND c.status IN ('FILLED','EXTERNAL') AND c.price_actual IS NOT NULL
    LEFT JOIN broker_symbol_map bsm
      ON o.venue_type IN ('mt4','mt5') AND bsm.venue = o.venue AND bsm.broker_symbol = o.symbol
    WHERE o.position_id BETWEEN 497 AND 507
      AND o.phase = 'OPEN' AND o.status = 'FILLED' AND o.price_actual IS NOT NULL
    GROUP BY o.position_id
),
cf AS (
    SELECT position_id, SUM(amount_usd) AS amt FROM position_cashflows
    WHERE position_id BETWEEN 497 AND 507
      AND (kind IN ('funding','swap')
           OR (kind = 'commission' AND event_key LIKE 'leg:%'))
    GROUP BY position_id
)
UPDATE positions p SET realized_pnl = COALESCE(pr.pnl,0) + COALESCE(cf.amt,0)
FROM price pr LEFT JOIN cf ON cf.position_id = pr.position_id
WHERE p.id = pr.position_id AND p.id BETWEEN 497 AND 507 AND p.status = 'CLOSED';

UPDATE position_settlements s
SET net_realized_usd   = p.realized_pnl,
    realized_usd       = p.realized_pnl - COALESCE(cf.amt,0),
    cashflows_net_usd  = cf.amt,
    price_pnl_usd      = p.realized_pnl - COALESCE(cf.amt,0)
FROM positions p
LEFT JOIN (
    SELECT position_id, SUM(amount_usd) AS amt FROM position_cashflows
    WHERE position_id BETWEEN 497 AND 507
      AND (kind IN ('funding','swap')
           OR (kind = 'commission' AND event_key LIKE 'leg:%'))
    GROUP BY position_id
) cf ON cf.position_id = p.id
WHERE p.id = s.position_id AND p.id BETWEEN 497 AND 507 AND p.status = 'CLOSED';
