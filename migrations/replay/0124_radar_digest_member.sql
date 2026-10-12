-- D-392 S1：radar digest 成员级边沿化状态表（新入才推+flap 24h TTL）。
-- 取代 radar_digest_state 的集合 hash 口径（成员进出阈值边界=集合抖动每轮触发；
-- 真信号=新成员进场）。老表停写保留不删（非破坏、可回滚参照）。
CREATE TABLE IF NOT EXISTS radar_digest_member (
  kind           TEXT NOT NULL,
  member         TEXT NOT NULL,
  last_pushed_at TIMESTAMPTZ,          -- NULL=进场未推过（出现在集合但推送失败/未达边沿）
  in_prev_set    BOOLEAN NOT NULL,     -- 上一轮 cur 是否含此成员
  PRIMARY KEY (kind, member)
);
