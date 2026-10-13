-- 1016_opp_eval_uneval_streak.sql —— auto 轨僵尸提案处置②：unevaluable 连拍计数。
-- 评估证据持续不足的提案不许无限挂 proposed——连拍计数由 UpsertOppEval 维护
-- （unevaluable→+1，其他 verdict→归零），连拍≥阈值由扫描拍翻 deferred 留痕。
ALTER TABLE fin_opp_eval ADD COLUMN IF NOT EXISTS uneval_streak integer NOT NULL DEFAULT 0;
COMMENT ON COLUMN fin_opp_eval.uneval_streak IS '连续 unevaluable 拍数（UpsertOppEval 维护；≥3 由 oppmatch 翻 deferred）';
