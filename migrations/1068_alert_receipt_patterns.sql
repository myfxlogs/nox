-- 1068_alert_receipt_patterns.sql —— 告警两级语义修：回执类消息前缀判据表化
-- （逗分前缀清单；命中者推一次即闭不进 24h「仍待确认」重推——D-168 episode 域）。
INSERT INTO app_config (key, value, note) VALUES
  ('alert_receipt_patterns','申购成交,已交割,已结清,已平仓,划转完成,接货已处置',
   '回执类告警消息子串（逗分 Contains——终态事件知会推一次即闭，免「仍待确认」重推误读；真文案形「EARN/DCI x 申购成交」「已交割（A → B）」）')
ON CONFLICT (key) DO NOTHING;
