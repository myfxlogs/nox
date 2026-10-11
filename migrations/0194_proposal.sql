-- D-189：提案实体化——fin_proposal 行即稳定 id/状态机/审计锚（params=快照非钉价）。
CREATE TABLE IF NOT EXISTS fin_proposal (
    id           bigserial PRIMARY KEY,
    template_id  text NOT NULL,
    exec_track   text NOT NULL,
    venue        text NOT NULL DEFAULT 'binance',
    params       jsonb NOT NULL DEFAULT '{}'::jsonb,
    expected_apr double precision,
    status       text NOT NULL DEFAULT 'proposed'
                 CHECK (status IN ('proposed','executed','expired','declined')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    decided_at   timestamptz
);
CREATE INDEX IF NOT EXISTS fin_proposal_status_idx ON fin_proposal(status, template_id);
-- 同模板 pending 提案单行（稳定 id 幂等锚——日扫复用不重开）。
CREATE UNIQUE INDEX IF NOT EXISTS fin_proposal_pending_uniq
    ON fin_proposal(template_id) WHERE status='proposed';

-- D-189：订单回链提案（proposal→order 审计链）。
ALTER TABLE fin_dci_order
    ADD COLUMN IF NOT EXISTS proposal_id bigint REFERENCES fin_proposal(id) ON DELETE SET NULL;
