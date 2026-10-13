-- 1015_watch_rule.sql —— 业主视窗两件套 B：通用盯盘规则表（业主令 10-14）。
-- 首期 kind='price'；subject=canonical 报价键（TRXUSDT）；venue_key 空=全源。
-- 评估器事件驱动（QuoteBus tick 即判，D-004）；once 触发自翻 disabled；repeat 走 cooldown。
CREATE TABLE IF NOT EXISTS watch_rule (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind          text NOT NULL CHECK (kind IN ('price')),           -- 留 kind 扩列（funding/nav 类后续）
    subject       text NOT NULL CHECK (subject <> ''),               -- canonical symbol
    venue_key     text NOT NULL DEFAULT '',                          -- '':全源；非空=该 venue:type 腿
    op            text NOT NULL CHECK (op IN ('lt','le','gt','ge')),
    threshold     double precision NOT NULL,
    fire_mode     text NOT NULL DEFAULT 'once' CHECK (fire_mode IN ('once','repeat')),
    cooldown_sec  integer NOT NULL DEFAULT 0 CHECK (cooldown_sec >= 0),
    note          text NOT NULL DEFAULT '',
    enabled       boolean NOT NULL DEFAULT true,
    last_fired_at timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
COMMENT ON TABLE watch_rule IS '业主盯盘规则：kind/subject/op/threshold 全 DB 驱动零硬编码';
COMMENT ON COLUMN watch_rule.subject IS 'canonical 报价键（Quote.Symbol 同空间）';
COMMENT ON COLUMN watch_rule.venue_key IS 'venue:type 复合键（keydomain.Join 同空间）；空=全源';
CREATE INDEX IF NOT EXISTS idx_watch_rule_subject ON watch_rule (subject) WHERE enabled;
