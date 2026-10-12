-- 0134_autotrade_allow_rules.sql —— D-443 S1：autotrade venue/canonical 执行白名单表化热读。
-- env→表迁移（账号 broker_accounts / 组合 xasset_allow_combos 已表化先例）：行只经业主
-- SQL 手写（无 UI/RPC 写路径=深思语义保留），engine 60s 周期热重载免重启（D-403 同模式）。
CREATE TABLE IF NOT EXISTS autotrade_allow_rules (
  id          BIGSERIAL PRIMARY KEY,
  kind        TEXT NOT NULL CHECK (kind IN ('venue','canonical')),
  value       TEXT NOT NULL,
  enabled     BOOL NOT NULL DEFAULT true,
  simulated   BOOL NOT NULL DEFAULT false,
  note        TEXT NOT NULL DEFAULT '',
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (kind, value, simulated)
);
-- seed 全镜像 env 现状：VENUES=10 行；CANONICALS=空（零行=全放行语义保真，D-122）。
INSERT INTO autotrade_allow_rules (kind, value, enabled, simulated, note) VALUES
  ('venue','277842155:mt5',true,false,'D-443 env迁移'),
  ('venue','591959217:mt5',true,false,'D-443 env迁移'),
  ('venue','52993526:mt5',true,false,'D-443 env迁移'),
  ('venue','39050818:mt5',true,false,'D-443 env迁移'),
  ('venue','15032313:mt5',true,false,'D-443 env迁移'),
  ('venue','59892915:mt5',true,false,'D-443 env迁移'),
  ('venue','3001157:mt5',true,false,'D-443 env迁移'),
  ('venue','okx:perp',true,false,'D-443 env迁移'),
  ('venue','binance:perp',true,false,'D-443 env迁移'),
  ('venue','gate:perp',true,false,'D-443 env迁移')
ON CONFLICT DO NOTHING;
