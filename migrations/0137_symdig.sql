-- 0137_symdig.sql - D-464 S1/S2：symbol 别名表 + symdig 深挖分类 findings 表。

-- fin_symbol_aliases：语义别名→canonical（GOLD→XAU 类）。数据驱动可审计；
-- 消费=scan 归一层 + symdig 分类器（载入检测循环拒收）。种子行仅格式示例，非生产语义。
CREATE TABLE IF NOT EXISTS fin_symbol_aliases (
	alias       text PRIMARY KEY,
	canonical   text NOT NULL,
	note        text NOT NULL DEFAULT '',
	created_at  timestamptz NOT NULL DEFAULT now()
);
INSERT INTO fin_symbol_aliases (alias, canonical, note) VALUES
	('GOLD', 'XAU', 'seed-example'),
	('GER40', 'DE30', 'seed-example')
ON CONFLICT (alias) DO NOTHING;

-- fin_symdig_findings：每轮 diff 快照（run_ts 同轮同值）。klass∈
-- {paired,unpaired,disabled,unmapped_unknown,mispaired}；payload=证据 JSONB。
CREATE TABLE IF NOT EXISTS fin_symdig_findings (
	run_ts   timestamptz NOT NULL,
	venue    text NOT NULL,
	symbol   text NOT NULL,
	klass    text NOT NULL,
	reason   text NOT NULL DEFAULT '',
	payload  jsonb NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX IF NOT EXISTS fin_symdig_findings_run_idx ON fin_symdig_findings (run_ts);
