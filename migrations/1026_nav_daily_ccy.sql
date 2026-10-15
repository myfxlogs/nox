-- 1026_nav_daily_ccy.sql —— D-522：fin_nav_daily 主键升 (day_start,currency)。
-- USD/USDT 各一行分账（跨币永不加总 D-285）；存量 USD-only 行 currency='USD' 保留不迁。
ALTER TABLE fin_nav_daily ADD COLUMN IF NOT EXISTS currency text NOT NULL DEFAULT 'USD';
ALTER TABLE fin_nav_daily DROP CONSTRAINT fin_nav_daily_pkey;
ALTER TABLE fin_nav_daily ADD PRIMARY KEY (day_start, currency);
