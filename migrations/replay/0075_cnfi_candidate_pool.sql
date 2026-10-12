-- 0075_cnfi_candidate_pool.sql - D-247 P1 候选池对比（SSOT=docs/cnfi/D-247-P1施工提示词-含v1.1修订-2026-09-06-Claude.md §2）
-- ljjz=累计净值（收益计算基准，分红调整天然覆盖；NULL=源未披露）；layer=候选层归属（NULL=非候选）；
-- caliber_break_date=LJJZ 口径断点日（R1：转型/折算史标记，跨断点区间指标=缺失）。
-- 种子=候选池 v1 冻结 8 只（业主令名单权下放，Claude 裁定；数据=东财 F10 实拍 2026-09-06；公开数据入 git 合规）。

ALTER TABLE cnfi_nav ADD COLUMN ljjz numeric;
ALTER TABLE cnfi_instruments ADD COLUMN layer text;
ALTER TABLE cnfi_instruments ADD COLUMN caliber_break_date date;

INSERT INTO cnfi_instruments (code, kind, name, is_active, layer) VALUES
  ('530028','fund','建信短债C',true,'liquidity'),
  ('005993','fund','光大超短债C',true,'liquidity'),
  ('485022','fund','工银尊益中短债F',true,'coupon'),
  ('485122','fund','工银尊益中短债C',true,'coupon'),
  ('202305','fund','南方旺元60天滚动中短债A',true,'coupon'),
  ('519199','fund','万家家享中短债A',true,'coupon'),
  ('090023','fund','大成安汇金融债C',true,'opportunity'),
  ('070026','fund','嘉实信用债券C',true,'opportunity')
ON CONFLICT (code) DO NOTHING;
