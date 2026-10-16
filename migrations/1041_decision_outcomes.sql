-- 裁定归因簿（D-561 决策席问责机制②系统件）：真金级裁定一行一账，
-- 关联其后 position_settlements 净额滚动 30d 出裁定质量指标+连败/净亏告警。
-- 选型=新表非复用结算簿：结算簿=每仓一行事实账（position_id 键），裁定=决策事件
-- （类别×对象键），粒度/键域/生命周期不同；复用=决策语义错挂结算行。
-- 物化径=扫描幂等（UNIQUE 守卫 ON CONFLICT DO NOTHING）：裁定源行
-- （carry_graduations approved / fin_proposal executed）本身携带全部决策事实
-- （decided_by/decided_at/structure/combo_id），单漏斗零漏路径，部署首拍回填存量已批行。
-- 归因快照列（attrib_*）由周期 runner 刷新——归因是呈现非裁决（不自动改判据/不禁用模板）。
CREATE TABLE IF NOT EXISTS fin_decision_outcomes (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  category text NOT NULL CHECK (category IN ('graduation_approved','gate_exempt','proposal_go')),
  object_key text NOT NULL,
  title text NOT NULL DEFAULT '',
  decision_ref text NOT NULL DEFAULT '',
  strategy_key text NOT NULL DEFAULT '',
  canonical text NOT NULL DEFAULT '',
  attrs jsonb NOT NULL DEFAULT '{}'::jsonb,
  decided_at timestamptz NOT NULL DEFAULT now(),
  attrib_net_usd double precision,
  attrib_positions integer,
  attrib_wins integer,
  attrib_losses integer,
  attrib_streak integer,
  alert_state text NOT NULL DEFAULT '',
  alert_at timestamptz,
  CONSTRAINT fin_decision_outcomes_object_uq UNIQUE (category, object_key)
);
COMMENT ON TABLE fin_decision_outcomes IS 'D-561 裁定归因簿：真金级裁定→其后 30d 结算净额归因（呈现非裁决）';
COMMENT ON COLUMN fin_decision_outcomes.category IS '裁定类别闭集：graduation_approved/gate_exempt/proposal_go';
COMMENT ON COLUMN fin_decision_outcomes.object_key IS '类别内对象键：grad:<id>/gate:<paper_id>/proposal:<id>（UNIQUE 幂等锚）';
COMMENT ON COLUMN fin_decision_outcomes.decision_ref IS '裁决主体引用：decided_by（''=人工 RPC→committee_rpc）/dciexec';
COMMENT ON COLUMN fin_decision_outcomes.strategy_key IS '归因关联键=positions.strategy_key 同域（structure→map 或 template_id）';
COMMENT ON COLUMN fin_decision_outcomes.canonical IS '标的窄化键（graduation.Symbol；空=模板级归因）';
COMMENT ON COLUMN fin_decision_outcomes.attrib_net_usd IS '30d 滚动归因净额（其后结算 net_realized_usd 合计，快照非事实源）';
COMMENT ON COLUMN fin_decision_outcomes.attrib_streak IS '当前负连败笔数（settled_at 倒序连续 net<0，0=无连败）';
COMMENT ON COLUMN fin_decision_outcomes.alert_state IS '告警边沿态：''|ok|net_loss|streak|组合（变化沿才推 warn 防刷屏）';
