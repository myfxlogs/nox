-- D-179：positions 加 notional_usd 列（gross exposure 真实化）。
-- 语义：该 position 全部腿名义合计（USD，成交价口径；SWAPARB=双腿=2×单腿名义）。
-- NULL 允许（历史仓 + 未落盘的新仓初始态）；gate 额度门 nil 计 0 + WARN。

ALTER TABLE positions ADD COLUMN IF NOT EXISTS notional_usd double precision;

-- 回填活跃仓 notional_usd（动态按 legs，禁止写死 position_id——test 库 id 不同）。
-- CS=100000 仅对 FX 成立（当前活跃仓全 FX）；USDJPY=147.0 一次性常数，回填误差 <1% 可接受。
-- migration 对 test 库幂等无害（无活跃仓则 0 行更新）。
UPDATE positions p SET notional_usd = sub.n
FROM (
  SELECT position_id, sum(qty * 100000 * COALESCE(price_actual, price_expected) / 147.0) AS n
  FROM position_legs WHERE phase = 'OPEN'
  GROUP BY position_id
) sub
WHERE p.id = sub.position_id
  AND p.status IN ('OPEN','OPENING','LEGGED','CLOSING');
