-- 0186_elim_evidence_seed.sql D-180：统计证据阈值 app_config 播种（ON CONFLICT 幂等）。
-- ARB_ELIM_ZSIGMA：淘汰判死 reason 置信下界 Zσ（B.12，默认 2）。
-- ARB_RETRO_DEP_Z：sysretro 转折点检验异常标注 Z 限（C.01/C.02，默认 2=95.45%）。
INSERT INTO app_config (key, value, note) VALUES
  ('ARB_ELIM_ZSIGMA','2','D-180：淘汰证据置信下界 Zσ（B.12 证据字段非判据）'),
  ('ARB_RETRO_DEP_Z','2','D-180：周报依赖性检验异常标注 Z 限（C.01/C.02 转折点检验）')
ON CONFLICT (key) DO NOTHING;
