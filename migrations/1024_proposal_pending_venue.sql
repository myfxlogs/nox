-- 1024_proposal_pending_venue.sql —— earn 执行径多 venue 化：
-- fin_proposal pending 唯一锚 (template_id)→(template_id,venue)——
-- 同模板异 venue 提案各行其位（binance 在册 pending 不再挡 okx/gate 产案）。
DROP INDEX IF EXISTS fin_proposal_pending_uniq;
CREATE UNIQUE INDEX fin_proposal_pending_uniq ON fin_proposal (template_id, venue)
  WHERE status = 'proposed';
