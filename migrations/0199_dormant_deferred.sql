-- 0199_dormant_deferred.sql —— D-204 休眠轨机器可读化：
-- ①fin_proposal status CHECK 加 'deferred'（declined=人拒/expired=失配过期语义皆错，
--   休眠=系统侧挂起独立终态）；②休眠轨种子入约束表（生产 id=10 已在账——幂等撞键跳）。
ALTER TABLE fin_proposal DROP CONSTRAINT IF EXISTS fin_proposal_status_check;
ALTER TABLE fin_proposal ADD CONSTRAINT fin_proposal_status_check
    CHECK (status IN ('proposed','executed','expired','declined','deferred'));

INSERT INTO fin_capability_constraint (scope, position_id, subject, qualifier, value_num, note, source)
VALUES ('bucket', NULL, 'dormant:dci_sell_high_covered_call@binance', 'always', 1,
        'D-204 休眠轨：covered_call@binance 停新提案+存量翻 deferred（D-191R7 业主令）', 'D-204')
ON CONFLICT (scope, (COALESCE(position_id, 0)), subject) DO NOTHING;
