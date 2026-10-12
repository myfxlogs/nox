-- 0200_dci_settle.sql —— D-207：DCI 交割观察径。
-- ①status CHECK 加 'settled'（交割终态独立——executed 语义是提案非订单，
--   failed=申购败语义皆错）；②settle_* 四列记 venue 回执实拍
--   exercisedCoin 行权方向+本息额（settleDate 仍在 response 原文不抽列——
--   回执原值语义保，仅观察结果落列）。
ALTER TABLE fin_dci_order DROP CONSTRAINT IF EXISTS fin_dci_order_status_check;
ALTER TABLE fin_dci_order ADD CONSTRAINT fin_dci_order_status_check
    CHECK (status IN ('pending','filled','failed','rejected','settled'));

ALTER TABLE fin_dci_order ADD COLUMN IF NOT EXISTS settle_ccy    text;
ALTER TABLE fin_dci_order ADD COLUMN IF NOT EXISTS settle_qty    numeric(30,10);
ALTER TABLE fin_dci_order ADD COLUMN IF NOT EXISTS settle_income numeric(30,10);
ALTER TABLE fin_dci_order ADD COLUMN IF NOT EXISTS settled_at    timestamptz;
