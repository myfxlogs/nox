-- 0191_capability_permission.sql D-188②：权限探测面——各所能力端点低频探活
-- 只读结果入账（state=present|absent|unknown；venue API 能力查询/受控只读试调，零下单）。
CREATE TABLE IF NOT EXISTS fin_capability_permission (
  id         bigserial PRIMARY KEY,
  venue      text NOT NULL,
  permission text NOT NULL,               -- dual_order / earn_subscribe / ...
  state      text NOT NULL CHECK (state IN ('present','absent','unknown')),
  probed_at  timestamptz NOT NULL DEFAULT now(),
  note       text NOT NULL DEFAULT '',
  UNIQUE (venue, permission)
);
