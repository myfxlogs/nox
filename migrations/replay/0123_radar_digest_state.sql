-- D-389 S1：universe-radar digest 行边沿化状态表。
-- 每类 digest（legless_band/class_filtered/real_leg/suspect）存集合内容 hash，
-- 本轮集合 hash==stored → 行不生成不推；不同 → 推送成功后 upsert。
CREATE TABLE IF NOT EXISTS radar_digest_state (
  kind         TEXT PRIMARY KEY,
  content_hash TEXT NOT NULL,
  pushed_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
