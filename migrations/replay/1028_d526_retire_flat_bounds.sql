-- D-526 返修③：平 OTM 界死行清退——dci_*_otm_bound 已被 σ 驱动界
-- （dci_*_otm_k + SigmaBoundRatio）替代，消费径 OTMBoundRatios/RatioOf
-- 同步删（零调用实证）。行删不留尸防误读。
DELETE FROM fin_capability_constraint
WHERE scope='global' AND subject IN ('dci_put_otm_bound','dci_call_otm_bound');
