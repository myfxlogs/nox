-- 1043_combo_pitgate.sql —— D-587④ 续：armed 坑签名闸（enable 写径物理拒
-- +评估环自动摘+面板透出）；pit_exempt_d=D# 显式豁免引用落行（误判可开豁
-- 但须留痕——同 paper_gate_ref 语义，禁默认）。
ALTER TABLE xasset_allow_combos ADD COLUMN IF NOT EXISTS pit_exempt_d text;
