-- D-264 飞书告警推送：单行表存推送目标（chat_id/open_id，从事件订阅自动提取）。
-- 单行约束 id=1；空值=未绑定（业主给机器人发首条消息后回填）。
CREATE TABLE IF NOT EXISTS feishu_push_target (
  id INT PRIMARY KEY DEFAULT 1,
  chat_id TEXT NOT NULL DEFAULT '',
  open_id TEXT NOT NULL DEFAULT '',
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT feishu_push_target_single_row CHECK (id = 1)
);
INSERT INTO feishu_push_target (id) VALUES (1) ON CONFLICT (id) DO NOTHING;
