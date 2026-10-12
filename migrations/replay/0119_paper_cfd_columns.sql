-- 0119_paper_cfd_columns.sql - D-374 S2：纸面轨扩 cfd×perp（structure/mt_venue 两列）。
-- structure 缺省 'spot_carry' 保既有行语义零回填；mt_venue NULL=spot_carry，
-- cfd 行记 CFD 腿 venue（行键 venue=mt×cex 组合串，见 StepPaperCfdCarry）。
ALTER TABLE paper_spot_carry ADD COLUMN IF NOT EXISTS structure TEXT NOT NULL DEFAULT 'spot_carry';
ALTER TABLE paper_spot_carry ADD COLUMN IF NOT EXISTS mt_venue TEXT;
