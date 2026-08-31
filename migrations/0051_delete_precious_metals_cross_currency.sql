-- D-176：broker_symbol_map 大清洗（normalize 开放前缀误匹配污染）。
-- D-175 原 0051 只清理贵金属 38 行；D-176 扩展为三条 DELETE 覆盖全部 97 行。
-- 1. 股票代码（含 . 的 rule 行）误归一到商品/指数 canonical
-- 2. 贵金属跨币种（XAGAUD/XAUAUD 等非 USD 计价）误归一到 SILVER/XAUUSD
-- 3. 已知错误变体（GOLDEURO/GOLDgr/GOLDoz/SILVEREURO/XAUXAG 等）

-- 第一条：股票代码污染（含 . 的全部 rule 行）——57 行
DELETE FROM broker_symbol_map
WHERE source = 'rule' AND broker_symbol ~* '\.';

-- 第二条：贵金属跨币种（非 USD 计价）——保留条件无 $ 锚点，保留 SILVER365/GOLD365
DELETE FROM broker_symbol_map
WHERE canonical IN ('SILVER', 'XAUUSD', 'XPDUSD', 'XPTUSD', 'XCUUSD')
  AND broker_symbol !~* '^(xag|xau|xpd|xpt|xcu)usd'
  AND broker_symbol !~* '^(silver|gold|palladium|platinum|copper)';

-- 第三条：已知错误变体（GOLDEURO/GOLDgr/GOLDoz/SILVEREURO/XAUXAG/XAGCAD/XAGCHF/XAGNZD）
DELETE FROM broker_symbol_map
WHERE canonical IN ('SILVER', 'XAUUSD')
  AND broker_symbol ~* '^(goldeuro|goldgr|goldoz|silvereuro|xauxag|xagcad|xagchf|xagnzd)$';
