-- 0190_condition_gap.sql D-186③：条件缺口表——模板×账本匹配出缺什么
-- → 请求文案 → requested/fulfilled 态机（飞书通道复用 D-168 episode 闸，告警行承载推送）。
CREATE TABLE IF NOT EXISTS fin_condition_gap (
  id           bigserial PRIMARY KEY,
  template_id  text NOT NULL DEFAULT '',            -- 触发模板（fin_opp_template.id；账本自检可空串）
  kind         text NOT NULL,                       -- credential/capital/permission/constraint/holding
  subject      text NOT NULL,                       -- venue/ccy/endpoint/field/asset
  qualifier    text NOT NULL DEFAULT '',
  request_text text NOT NULL,                       -- 给业主的请求文案（开户/凭据/权限/划转/约束喂入）
  status       text NOT NULL DEFAULT 'open'
               CHECK (status IN ('open','requested','fulfilled','waived')),
  created_at   timestamptz NOT NULL DEFAULT now(),
  requested_at timestamptz,
  resolved_at  timestamptz,
  UNIQUE (template_id, kind, subject)
);
