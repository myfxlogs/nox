-- 1046_earn_yield_fanout_reversal.sql —— earn_yield 扇出假账冲正（STATE「binance
-- earn_yield 归属扇出修复」票·存量核销段；代码段=行级 (venue,ccy) 流扣同票另落）。
--
-- 病灶（SG 10-17Z 取证定谳）：attributeEarnYield 逐币行扣 venue 级申购流
-- （EarnSubscribeSum 无 ccy 维度）——USDT 申购 #32=351.64289268 / #33=#34=0.05479
-- 被从每个非 USDT 币种行各扣一遍（D-516 share 同乘变体：BTC 行 −351.64×0.3334
-- =−117.23774 实测分毫不差），累计 −30,367.21/3701 行。
--
-- 冲正纪律（票面③）：禁删史——反向镜像行对消；金额零手编（全部 -amount_usd 镜像，
-- 谓词从表内数据机械重构）。镜像行 tran_id=−原 tran_id（原值恒正无碰撞）、
-- ts=原 ts（SumEarnYieldSince 任意 since 窗内成对对消）、income_type 不变
-- （既有读者 SUM 自然对消）；tran_id<0 即冲正 provenance。
--
-- ①扇出族：按病灶公式机械重构——逐行归属窗 prevTS（快照逐币前值，7d 保留窗内
--   行全在，与产假账时读到的同一行）×窗内 filled EARN 申购和 F×share（D-516
--   app_config ownership_share 缺省 1）；|amount+F×share|≤0.05（真收益残差实测域
--   ≤0.03）→ 镜像。实测覆盖 261 行：86×−351.64 + ~172×−0.0548 + BTC 份额变体
--   −117.23774/−0.018267×2 + DOGE/PEPE 真收益搭行（真收益分量 ≤0.005 镜像过冲，
--   量级 $1e-4 以下如实注记）。
-- ②USDT 本金往返伪影三笔（id 189/240/347，SG 行号取证锚 10-17Z，其他库无此 id=自然空转）：
--   +1799.0018=DCI SETTLED 虚计入池（4b4866fe8 前病灶瞬时 NAV+1441 落账）／
--   −1440.998=该病灶修后回吐镜像／−357.9951=DCI#11(18549966) 到期本金出池记负收益。
--   三笔皆本金移动非收益，须成组冲正：净额 +0.0087，单冲任一笔会把
--   SumEarnYieldSince 窗口炸向 ±1799（actual_apr 口径污染）。
-- ③0.00 尘币存量行（3410 行净 +0.34）不动：非假账（真实微增量），前向零额滤
--   同票代码段杜绝新增。
WITH bug AS (
    SELECT r.id, r.venue, r.venue_type, r.income_type, r.symbol, r.amount_usd,
           r.tran_id, r.exchange_config_id, r.ts,
           COALESCE((SELECT max(s.fetched_at) FROM fin_venue_account_snapshot s
                     WHERE s.venue = 'binance' AND s.venue_type = 'earn'
                       AND s.currency = r.symbol AND s.fetched_at < r.ts),
                    '-infinity'::timestamptz) AS prev_ts
    FROM income_events r
    WHERE r.venue = 'binance' AND r.venue_type = 'earn'
      AND r.income_type = 'earn_yield' AND r.symbol <> 'USDT'
),
flowed AS (
    SELECT b.*,
           COALESCE((SELECT SUM(o.qty) FROM fin_dci_order o
                     WHERE o.venue = 'binance' AND o.opt_type = 'EARN'
                       AND o.status = 'filled'
                       AND o.created_at > b.prev_ts AND o.created_at <= b.ts), 0) AS wrong_flow
    FROM bug b
),
fanned AS (
    SELECT f.* FROM flowed f
    WHERE f.wrong_flow > 0 AND f.amount_usd < -0.005
      AND abs(f.amount_usd + f.wrong_flow * COALESCE(
            (SELECT c.value::float8 FROM app_config c
             WHERE c.key = 'ownership_share:binance:earn:' || f.symbol), 1)) <= 0.05
),
reversals AS (
    SELECT venue, venue_type, exchange_config_id, symbol, income_type, amount_usd, tran_id, ts
    FROM fanned
    UNION ALL
    SELECT venue, venue_type, exchange_config_id, symbol, income_type, amount_usd, tran_id, ts
    FROM income_events
    WHERE venue = 'binance' AND venue_type = 'earn' AND income_type = 'earn_yield'
      AND id IN (189, 240, 347)
)
INSERT INTO income_events
    (venue, venue_type, exchange_config_id, symbol, income_type, amount_usd, tran_id, ts)
SELECT venue, venue_type, exchange_config_id, symbol, income_type, -amount_usd, -tran_id, ts
FROM reversals
ON CONFLICT (exchange_config_id, tran_id) DO NOTHING;
