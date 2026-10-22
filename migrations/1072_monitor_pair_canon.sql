-- 1072 monitor 簿 pair 键归一·存量回填（S2 方言 canonical 病灶——
-- #26-31 组合 canonical=XAG_USDT/XAG-USDT-SWAP/NAS100_USDT/US30_USDT/
-- SPX500_USDT 方言形写入 pair 左段，消费侧归一键联查零行假象）。
-- pair 形=<cexCanon>-<mtBare>：右段=最后 '-' 后 mt 裸形无分隔符；左段归一
-- ='_'/'-'全剥（含 -SWAP 自然消解）——等价 BookCanonical 键域语义。
-- 幂等可重放（计算值相等行零更新）。
UPDATE fin_xasset_monitor m
SET pair = n.p
FROM (
  SELECT id,
    replace(replace(
      left(pair, length(pair) - strpos(reverse(pair), '-')),
      '_', ''), '-', '')
    || '-' || right(pair, strpos(reverse(pair), '-') - 1) AS p
  FROM fin_xasset_monitor
  WHERE strpos(reverse(pair), '-') > 0
) n
WHERE m.id = n.id AND m.pair <> n.p;
