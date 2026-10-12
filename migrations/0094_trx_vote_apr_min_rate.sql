-- D-297：fin_trx_vote_apr 加 min_rate/min_sr 列（投票 APR 口径改最低年化）
ALTER TABLE fin_trx_vote_apr
  ADD COLUMN min_rate double precision NOT NULL DEFAULT 0,
  ADD COLUMN min_sr character varying(64);
