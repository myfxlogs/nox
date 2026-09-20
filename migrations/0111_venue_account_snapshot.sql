-- 0111_venue_account_snapshot.sql - D-358 S1：CEX/MT 账户余额快照落库（喂 D-356 资金不眠覆盖率面板）。
-- 数据源=cexscan.ScanAccounts（D-271 S1，AccountMonitor 统一真相源，CFD+CEX 全 venue）——
-- 内存扫描已存在，本表只补持久化（零新采集器）；append 语义保留曲线，周期任务无需去重。
-- 币种核证点（D-358 §S1 必做，实证 internal/reconcile/reconcile_queriers.go：
--   MT4/5 accountSummaryToInfo* Currency=sum.GetCurrency() 原币直传；
--   CEX cex_account.go Currency="USDT"）——原币混合实锤：列名 _raw+currency 列，
--   不假设 USD；折算另案（D-358 §4 边界：不折算币种，如实标）。
CREATE TABLE fin_venue_account_snapshot (
  id              bigserial PRIMARY KEY,
  venue           text NOT NULL,
  venue_type      text NOT NULL,
  balance_raw     double precision,
  equity_raw      double precision,
  free_margin_raw double precision,
  currency        text NOT NULL DEFAULT '',
  fetched_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ON fin_venue_account_snapshot (venue, fetched_at DESC);
