-- 0155 D-534 B4：idle_floor_streak——睡钱触发层连续天数状态表。
-- PK (venue,venue_type) 与 fin_venue_account_snapshot 池键同形；alerted=本轮
-- 告警已发（非 below 归零才复位，下轮重新计）；last_check_day=BJ 历日幂等锚。
CREATE TABLE IF NOT EXISTS idle_floor_streak (
  venue      TEXT NOT NULL,
  venue_type TEXT NOT NULL,
  consecutive_below_days INT NOT NULL DEFAULT 0,
  last_check_day DATE NOT NULL,
  alerted    BOOLEAN NOT NULL DEFAULT false,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (venue, venue_type)
);
