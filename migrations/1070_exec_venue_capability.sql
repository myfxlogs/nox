-- 1070：venue 可执行面归能力账本投影（D-651 A 段——exec_venue:<tmpl>@<venue>
-- 行=执行轨覆盖 venue 事实集，gate.DCIExecVenues/EarnExecVenues 裸 map 改读
-- 账本投影；venue 接径=落账本行非改共享码）。
-- scope='global'、qualifier=''、value_num=1 同 dormant 形。
INSERT INTO fin_capability_constraint (scope, subject, qualifier, value_num, note, source)
SELECT 'global', v.s, '', 1, 'D-651 venue 执行面投影（新 venue 接径=账本行非代码）', 'd651-proj'
FROM (VALUES
  ('exec_venue:dci_buy_low@binance'),
  ('exec_venue:dci_buy_low@okx'),
  ('exec_venue:dci_sell_high_covered_call@binance'),
  ('exec_venue:dci_sell_high_covered_call@okx'),
  ('exec_venue:cash_mgmt@binance'),
  ('exec_venue:cash_mgmt@okx'),
  ('exec_venue:cash_mgmt@gate'),
  ('exec_venue:earn_apr_lending@binance'),
  ('exec_venue:earn_apr_lending@okx'),
  ('exec_venue:earn_apr_lending@gate'),
  ('exec_venue:earn_rotation@binance'),
  ('exec_venue:earn_rotation@okx'),
  ('exec_venue:earn_rotation@gate')
) AS v(s)
ON CONFLICT (scope, (COALESCE(position_id,0)), subject) DO NOTHING;
