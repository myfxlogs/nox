-- D-249 S3：全 SR top5 年化持久化（身份争议逐 SR 复核）。
-- apr.go 每 Poll 落 top-5 producer 行（5×288=1.4k 行/日，无保留压力）。
-- 0071 fin_trx_vote_apr 只存聚合行（current/max），无法逐 SR 复核（今晨 TronSpark↔Luganodes 身份争议）。
CREATE TABLE fin_trx_vote_apr_top (
    id              BIGSERIAL PRIMARY KEY,
    fetched_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    sr_name         VARCHAR(64) NOT NULL,
    annualized_rate DOUBLE PRECISION NOT NULL
);

CREATE INDEX idx_trx_vote_apr_top_fetched_at ON fin_trx_vote_apr_top (fetched_at DESC);
