-- D-191-R9：%OTM 闸界入约束表（leg.go 字面量出库——行缺=constraint 缺口同 custody_share 例）。
-- PUT 界 0.93→1.00 严格下界（币安轨真实用法=贴现货轮式）；CALL 保 1.10。
INSERT INTO fin_capability_constraint (scope, subject, qualifier, value_num, note, source) VALUES
  ('global','dci_call_otm_bound','min',1.10,'DCI CALL strike≥spot×界（卖高 OTM 下限）','D-191-R9'),
  ('global','dci_put_otm_bound','max',1.00,'DCI PUT strike≤spot×界（买低贴现货轮式）','D-191-R9')
ON CONFLICT DO NOTHING;
