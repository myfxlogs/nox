-- D-526：DCI 尾险定价闸约束行——σ 驱动 OTM 界（bound=1∓k·σ√t）+在仓名义帽。
-- 行缺=fail-closed 不产提案（同 custody_share 例）；k=1.5 按 10-15 决策席口径
-- （σ=3.5%/tenor=16d → 界≈0.79）；tenor_max=30d；名义帽字面 1500 USDT
-- （≈NAV_USDT×0.15 建议值，业主域可改）。
INSERT INTO fin_capability_constraint (scope, subject, qualifier, value_num, note, source) VALUES
  ('global','dci_put_otm_k','k',1.5,'DCI PUT OTM 界=spot×(1−k·σ_daily·√tenor)','D-526'),
  ('global','dci_call_otm_k','k',1.5,'DCI CALL OTM 界=spot×(1+k·σ_daily·√tenor)','D-526'),
  ('global','dci_tenor_max_days','max',30,'DCI 期限上限天数（σ 界 tenor 帽）','D-526'),
  ('global','dci_open_notional_cap_usdt','max',1500,'DCI 在仓名义合计帽 USDT（≈NAV_USDT×0.15）','D-526')
ON CONFLICT DO NOTHING;
