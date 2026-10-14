-- 1020_paper_delivery.sql —— 日历基差 P0 纸面轨：paper_spot_carry venue_type 枚举加
-- 'futures'（perp_delivery 结构腿=交割腿 futures×对冲腿 spot）。spot_carry 同式幂等。
ALTER TABLE public.paper_spot_carry DROP CONSTRAINT IF EXISTS psc_venue_type_ab_chk;
ALTER TABLE public.paper_spot_carry ADD CONSTRAINT psc_venue_type_ab_chk
    CHECK (((venue_type_a = ANY (ARRAY[''::text, 'spot'::text, 'perp'::text, 'futures'::text]))
        AND (venue_type_b = ANY (ARRAY[''::text, 'spot'::text, 'perp'::text, 'futures'::text]))));
