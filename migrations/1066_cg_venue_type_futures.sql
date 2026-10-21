-- 1066_cg_venue_type_futures.sql —— carry_graduations venue_type 枚举漂移修：
-- 基线 1000 已含 'futures'（就地改基线 commit ba84705ba/9cda5a412），但已部署实例
-- 活约束仍为 {''|spot|perp}（就地改不回流生产）——paper#67191（perp_delivery
-- ETH-USD_UM-261225，venue_type_a='futures' 全值合法）毕业插行 SQLSTATE 23514 被拒。
-- 对拍 1020 paper_spot_carry 同款修法：DROP+ADD 幂等，鲜装重放与升级安装终态一致。
ALTER TABLE public.carry_graduations DROP CONSTRAINT IF EXISTS cg_venue_type_ab_chk;
ALTER TABLE public.carry_graduations ADD CONSTRAINT cg_venue_type_ab_chk
    CHECK (((venue_type_a = ANY (ARRAY[''::text, 'spot'::text, 'perp'::text, 'futures'::text]))
        AND (venue_type_b = ANY (ARRAY[''::text, 'spot'::text, 'perp'::text, 'futures'::text]))));
