-- 0192_dci_order.sql D-191：DCI 申购执行轨幂等账本（request_id UNIQUE 防重发；
-- status 态机 pending→filled|failed|rejected；首版只 binance，凭据权限未知先探后写）。
CREATE TABLE IF NOT EXISTS fin_dci_order (
  id          bigserial PRIMARY KEY,
  request_id  text NOT NULL UNIQUE,         -- 幂等键：template|product|delivery|qty hash
  venue       text NOT NULL,
  template_id text NOT NULL DEFAULT '',
  product_ref text NOT NULL DEFAULT '',     -- venue 产品号（projectId）
  underlying  text NOT NULL,
  opt_type    text NOT NULL DEFAULT '',     -- C=卖高/P=买低
  strike      double precision,
  invest_ccy  text NOT NULL DEFAULT '',
  qty         double precision NOT NULL,
  tenor_days  int,
  status      text NOT NULL DEFAULT 'pending'
              CHECK (status IN ('pending','filled','failed','rejected')),
  response    text NOT NULL DEFAULT '',     -- venue 原始应答截断存证
  created_at  timestamptz NOT NULL DEFAULT now(),
  resolved_at timestamptz
);
