-- D-175：清理贵金属跨币种误归一映射。
-- normalize 规则 `^(xag|silver)` 误把 XAGAUD/XAGEUR 等归一到 SILVER，
-- `^(xau|gold)` 误把 XAUAUD/XAUXAG 等归一到 XAUUSD。
-- 本迁移删除非 USD 计价的贵金属映射，保留 USD 计价映射。
DELETE FROM broker_symbol_map
WHERE canonical IN ('SILVER', 'XAUUSD', 'XPDUSD', 'XPTUSD', 'XCUUSD')
  AND broker_symbol !~* '^(xag|xau|xpd|xpt|xcu)usd'
  AND broker_symbol !~* '^(silver|gold|palladium|platinum|copper)';
