-- 0150_explorer_shell.sql —— D-513 探矿员壳（设计稿 v1.2 §4）：记分板骨架双表。
-- explorer_proposals：LLM 提案记分板（status 席位手动回填流转，无自动迁移路径）；
-- explorer_state：跨进程状态快照（昨快照 diff 幂等用）。
-- scan_date 由 Go 侧按 BJ 历日算好写入（F1：DB timezone=Etc/UTC，proposed_at::date
-- 日界=UTC 午夜=BJ 08:00——日界钉代码不赌 DB 时区，D-510 同款）。

CREATE TABLE IF NOT EXISTS explorer_proposals (
  id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  scan_date   DATE NOT NULL,
  proposed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  scan_kind   TEXT NOT NULL CHECK (scan_kind IN ('daily','weekly')),
  source      TEXT NOT NULL DEFAULT 'explorer',
  theme       TEXT NOT NULL DEFAULT '',
  canonical   TEXT NOT NULL,
  best_pair   TEXT NOT NULL DEFAULT '',
  direction   TEXT NOT NULL DEFAULT '',
  rationale   TEXT NOT NULL DEFAULT '',
  evidence    JSONB NOT NULL DEFAULT '{}',
  status      TEXT NOT NULL DEFAULT 'new' CHECK (status IN ('new','submitted','admitted','denied','expired')),
  gate_note   TEXT NOT NULL DEFAULT '',
  settled_ref TEXT,
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
COMMENT ON TABLE explorer_proposals IS 'D-513 探矿员提案记分板（壳期提案止于收件箱，status 席位手动回填）';
COMMENT ON COLUMN explorer_proposals.scan_date IS 'BJ 历日（Go 侧算好写入，F1：禁 proposed_at::date 依赖 DB 时区）';
COMMENT ON COLUMN explorer_proposals.scan_kind IS '扫描类别：daily=日扫五项/weekly=周深挖';
COMMENT ON COLUMN explorer_proposals.source IS '提案来源（默认 explorer；B8 统一面并入时免反推）';
COMMENT ON COLUMN explorer_proposals.theme IS '周深挖主题（日扫空串）';
COMMENT ON COLUMN explorer_proposals.canonical IS '规范符号键（落库前校验命中已知集）';
COMMENT ON COLUMN explorer_proposals.best_pair IS '最优腿对 venue_a×venue_b 键形（空=无配对）';
COMMENT ON COLUMN explorer_proposals.direction IS '方向（≤16字，LLM 输出截断校验）';
COMMENT ON COLUMN explorer_proposals.rationale IS '一行理由（LLM 叙事）';
COMMENT ON COLUMN explorer_proposals.evidence IS '佐证数字 JSONB（键=输入段字段名）';
COMMENT ON COLUMN explorer_proposals.status IS '流转态：new/submitted/admitted/denied/expired（手动回填）';
COMMENT ON COLUMN explorer_proposals.gate_note IS '闸结果注记（席位回填）';
COMMENT ON COLUMN explorer_proposals.settled_ref IS '结算引用（席位回填）';

CREATE UNIQUE INDEX IF NOT EXISTS explorer_proposals_dedupe
  ON explorer_proposals (scan_date, scan_kind, canonical, best_pair);

CREATE TABLE IF NOT EXISTS explorer_state (
  key        TEXT PRIMARY KEY,
  val        JSONB NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
COMMENT ON TABLE explorer_state IS 'D-513 探矿员跨进程状态（昨快照/diff 幂等键）';
COMMENT ON COLUMN explorer_state.key IS '状态键（如 watchlist_snapshot/symbol_map_count）';
COMMENT ON COLUMN explorer_state.val IS '状态值 JSONB';
