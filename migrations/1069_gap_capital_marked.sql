-- 1069: fin_condition_gap kind=capital 回执核验环（D-652-C）
-- ①status 值域增 'marked'（人工已点·到账未实证中间态——fulfilled 自此仅实证收口）；
-- ②target_bucket/target_usd/marked_at 核验锚三列；
-- ③存量清扫：kind=capital 且「投资人页回执」出处 fulfilled 行降 marked（标的时点桶值
--   无基线可回算——base 缺位时核验径取绝对额判据 cur≥target×0.95，fail-closed）。
ALTER TABLE public.fin_condition_gap
    DROP CONSTRAINT IF EXISTS fin_condition_gap_status_check;
ALTER TABLE public.fin_condition_gap
    ADD CONSTRAINT fin_condition_gap_status_check
    CHECK (status = ANY (ARRAY['open'::text, 'requested'::text, 'marked'::text,
                              'fulfilled'::text, 'waived'::text]));
ALTER TABLE public.fin_condition_gap ADD COLUMN IF NOT EXISTS target_bucket text;
ALTER TABLE public.fin_condition_gap ADD COLUMN IF NOT EXISTS target_usd double precision;
ALTER TABLE public.fin_condition_gap ADD COLUMN IF NOT EXISTS marked_at timestamptz;
-- legacy 人工回执行降 marked（幂等——重跑零行命中）。
UPDATE public.fin_condition_gap
   SET status='marked', marked_at=COALESCE(resolved_at, now()), resolved_at=NULL
 WHERE kind='capital' AND status='fulfilled' AND resolve_note LIKE '投资人页回执%';
