-- 0173_paper_window_start.sql D-033①：paper 观测窗起点进程内化——
-- scripts/paper_window_report.sh 硬编码默认值（D-028 部署时刻 2026-10-03T01:59Z）
-- 改 app_config 单源，日报/Stage 闸与运维热改同一键。
-- ON CONFLICT DO NOTHING：已存运维覆盖行不盖（幂等可重放）。
INSERT INTO app_config (key, value, note) VALUES
  ('ARB_PAPER_WINDOW_START','2026-10-03T01:59Z','D-033：paper long_cfd_short_perp 观测窗起点（=D-028 部署时刻）')
ON CONFLICT (key) DO NOTHING;
