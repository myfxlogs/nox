-- s2bridge 写入侧 canonical 归一·存量回填（同族病灶根治：US500/TRXUSDT 类
-- 「簿侧归一/组合行存上游原值」→消费侧查无行假阳——坑条目
-- mt-canonical-normalize-consumer-side）。两阶：①MT 腿经 broker_symbol_map
-- 归一（broker 原名→canonical）；②残余 USDT/USD 尾裁（mtBareCanon 同款）。
-- MT 腿判=venue_key 尾缀 :mt5/:mt4（键实列禁裸名猜）。幂等可重放。
UPDATE xasset_allow_combos x SET canonical_sell = m.canonical
FROM broker_symbol_map m
WHERE (x.venue_key_sell LIKE '%:mt5' OR x.venue_key_sell LIKE '%:mt4')
  AND m.venue = x.venue_sell AND m.broker_symbol = x.canonical_sell
  AND x.canonical_sell <> m.canonical;
UPDATE xasset_allow_combos x SET canonical_buy = m.canonical
FROM broker_symbol_map m
WHERE (x.venue_key_buy LIKE '%:mt5' OR x.venue_key_buy LIKE '%:mt4')
  AND m.venue = x.venue_buy AND m.broker_symbol = x.canonical_buy
  AND x.canonical_buy <> m.canonical;
UPDATE xasset_allow_combos SET canonical_sell = regexp_replace(canonical_sell,'USDT$|USD$','')
WHERE (venue_key_sell LIKE '%:mt5' OR venue_key_sell LIKE '%:mt4') AND canonical_sell ~ 'USDT$|USD$';
UPDATE xasset_allow_combos SET canonical_buy = regexp_replace(canonical_buy,'USDT$|USD$','')
WHERE (venue_key_buy LIKE '%:mt5' OR venue_key_buy LIKE '%:mt4') AND canonical_buy ~ 'USDT$|USD$';
