-- 1067_venue_type_futures_sweep.sql —— venue_type 'futures' 全谱 CHECK 修（D-648：
-- perp_delivery 毕业径 #387 撞 xasset_venue_type_chk 23514——1066 只补
-- carry_graduations 一张表，同族枚举面整谱清扫防下颗雷）。
-- 域=8 值通道枚举（mt4|mt5|perp|spot|funding|earn|option|unified|''）补 'futures'
-- →9 值；ticks 族 4 值行情通道枚举补 'futures'→5 值。
-- 域值对拍：新域=旧域∪{futures} 严格超集——存量行天然全合规（迁出为零，票面对拍
-- 义务经断言覆盖）。broker_accounts_venue_type_chk（mt4|mt5）不补——MT 通道域
-- 语义内不含 futures，补之=放开错域。
-- DROP+ADD 幂等（1014/1020/1034/1066 同款），鲜装/升级终态逐字一致；禁就地改基线。

-- ① 8→9 值家族
ALTER TABLE public.autotrade_logs DROP CONSTRAINT IF EXISTS autotrade_logs_venue_type_chk;
ALTER TABLE public.autotrade_logs ADD CONSTRAINT autotrade_logs_venue_type_chk
    CHECK (((venue_type_buy = ANY (ARRAY['mt4'::text, 'mt5'::text, 'perp'::text, 'spot'::text, 'funding'::text, 'earn'::text, 'option'::text, 'unified'::text, 'futures'::text, ''::text]))
        AND (venue_type_sell = ANY (ARRAY['mt4'::text, 'mt5'::text, 'perp'::text, 'spot'::text, 'funding'::text, 'earn'::text, 'option'::text, 'unified'::text, 'futures'::text, ''::text]))));

ALTER TABLE public.facts DROP CONSTRAINT IF EXISTS facts_venue_type_chk;
ALTER TABLE public.facts ADD CONSTRAINT facts_venue_type_chk
    CHECK ((venue_type = ANY (ARRAY['mt4'::text, 'mt5'::text, 'perp'::text, 'spot'::text, 'funding'::text, 'earn'::text, 'option'::text, 'unified'::text, 'futures'::text, ''::text])));

ALTER TABLE public.income_events DROP CONSTRAINT IF EXISTS income_events_venue_type_chk;
ALTER TABLE public.income_events ADD CONSTRAINT income_events_venue_type_chk
    CHECK ((venue_type = ANY (ARRAY['mt4'::text, 'mt5'::text, 'perp'::text, 'spot'::text, 'funding'::text, 'earn'::text, 'option'::text, 'unified'::text, 'futures'::text, ''::text])));

ALTER TABLE public.opportunities DROP CONSTRAINT IF EXISTS opp_venue_type_chk;
ALTER TABLE public.opportunities ADD CONSTRAINT opp_venue_type_chk
    CHECK (((venue_type_buy = ANY (ARRAY['mt4'::text, 'mt5'::text, 'perp'::text, 'spot'::text, 'funding'::text, 'earn'::text, 'option'::text, 'unified'::text, 'futures'::text, ''::text]))
        AND (venue_type_sell = ANY (ARRAY['mt4'::text, 'mt5'::text, 'perp'::text, 'spot'::text, 'funding'::text, 'earn'::text, 'option'::text, 'unified'::text, 'futures'::text, ''::text]))));

ALTER TABLE public.position_cashflows DROP CONSTRAINT IF EXISTS position_cashflows_venue_type_chk;
ALTER TABLE public.position_cashflows ADD CONSTRAINT position_cashflows_venue_type_chk
    CHECK ((venue_type = ANY (ARRAY['mt4'::text, 'mt5'::text, 'perp'::text, 'spot'::text, 'funding'::text, 'earn'::text, 'option'::text, 'unified'::text, 'futures'::text, ''::text])));

ALTER TABLE public.position_legs DROP CONSTRAINT IF EXISTS position_legs_venue_type_chk;
ALTER TABLE public.position_legs ADD CONSTRAINT position_legs_venue_type_chk
    CHECK ((venue_type = ANY (ARRAY['mt4'::text, 'mt5'::text, 'perp'::text, 'spot'::text, 'funding'::text, 'earn'::text, 'option'::text, 'unified'::text, 'futures'::text, ''::text])));

ALTER TABLE public.position_swap_snapshots DROP CONSTRAINT IF EXISTS pss_venue_type_chk;
ALTER TABLE public.position_swap_snapshots ADD CONSTRAINT pss_venue_type_chk
    CHECK ((venue_type = ANY (ARRAY['mt4'::text, 'mt5'::text, 'perp'::text, 'spot'::text, 'funding'::text, 'earn'::text, 'option'::text, 'unified'::text, 'futures'::text, ''::text])));

ALTER TABLE public.xasset_allow_combos DROP CONSTRAINT IF EXISTS xasset_venue_type_chk;
ALTER TABLE public.xasset_allow_combos ADD CONSTRAINT xasset_venue_type_chk
    CHECK (((venue_type_buy = ANY (ARRAY['mt4'::text, 'mt5'::text, 'perp'::text, 'spot'::text, 'funding'::text, 'earn'::text, 'option'::text, 'unified'::text, 'futures'::text, ''::text]))
        AND (venue_type_sell = ANY (ARRAY['mt4'::text, 'mt5'::text, 'perp'::text, 'spot'::text, 'funding'::text, 'earn'::text, 'option'::text, 'unified'::text, 'futures'::text, ''::text]))));

-- ② ticks 族 4→5 值：父表 DROP+ADD——约束对分区表自动级联传播（全部
-- 存量活动分区同步换代；新建分区经 PARTITION OF 自动承父约束，无需枚举动表）。
ALTER TABLE public.ticks DROP CONSTRAINT IF EXISTS ticks_venue_type_chk;
ALTER TABLE public.ticks ADD CONSTRAINT ticks_venue_type_chk
    CHECK ((venue_type = ANY (ARRAY['mt4'::text, 'mt5'::text, 'perp'::text, 'spot'::text, 'futures'::text])));
