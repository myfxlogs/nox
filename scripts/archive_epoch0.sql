-- archive_epoch0.sql —— E0 纪元归档（业主令 10-13；终裁席裁「封存非删除」）。
-- 序列（铁序）：①pg_dump -Fc 全量副本先落两处（SG /opt/nox-backups/ + rclone R2）
-- 之前**禁跑本脚本**；②CREATE SCHEMA arch_e0；③全表归档集 CTAS→单语句
-- TRUNCATE ... RESTART IDENTITY（表间 FK 同事务覆盖）；④终态行集「在途留活」
-- DELETE 归；⑤fin_nav_daily 特例=归档全部+最新行回插（NAV 30d 回撤硬线基线锚）。
-- 歧义表裁定（禁线「歧义表→留活+LOG 标名不猜」执行）：
--   fin_proposal / explorer_proposals 由票面全表集**转终态行集**——SG 实拍含
--   proposed×5 / new×40 在途行，wiping 在途违反业主令「在途行留活」总语义；
--   fin_trx_cycle_vi / fin_trx_txs / fin_trx_vote_apr / fin_trx_vote_apr_top /
--   fin_trx_witness 五件**无 status 列**（快照/账本型）→ 留活不猜，标名待委员会。
-- 运行：psql -U nox -d nox -v ON_ERROR_STOP=1 -f archive_epoch0.sql
-- 安全：单事务全做或全不做；FK 撞守库面即回滚（留活表反向引用归档表会报错——
-- 执行前跑文末 pre-flight FK 查询确认零出边）。
\set ON_ERROR_STOP on

BEGIN;

-- 幂等守：arch_e0 已含行=曾跑过，拒再跑（重跑会 TRUNCATE 已空表=静默假成功）。
DO $$
BEGIN
	IF EXISTS (
		SELECT 1 FROM pg_tables WHERE schemaname = 'arch_e0' LIMIT 1
	) THEN
		RAISE EXCEPTION 'arch_e0 schema 已存在——归档疑似已跑，中止（E0 幂等闸）';
	END IF;
END $$;

CREATE SCHEMA arch_e0;

-- ============ ③ 全表归档集（CTAS 后同事务 TRUNCATE RESTART IDENTITY）============
-- CTAS=纯数据副本（约束/索引/序列不随——arch 是封存面非查询面，行数对拍为验收锚）。
CREATE TABLE arch_e0.alerts                    AS TABLE alerts;
CREATE TABLE arch_e0.autotrade_logs            AS TABLE autotrade_logs;
CREATE TABLE arch_e0.carry_graduations         AS TABLE carry_graduations;
CREATE TABLE arch_e0.cex_private_events        AS TABLE cex_private_events;
CREATE TABLE arch_e0.combo_daily_rollup        AS TABLE combo_daily_rollup;
CREATE TABLE arch_e0.execution_control_events  AS TABLE execution_control_events;
CREATE TABLE arch_e0.fin_cex_dci_quote         AS TABLE fin_cex_dci_quote;
CREATE TABLE arch_e0.fin_cex_events            AS TABLE fin_cex_events;
CREATE TABLE arch_e0.fin_cex_radar             AS TABLE fin_cex_radar;
CREATE TABLE arch_e0.fin_dept_reports          AS TABLE fin_dept_reports;
CREATE TABLE arch_e0.fin_llm_call              AS TABLE fin_llm_call;
CREATE TABLE arch_e0.fin_opp_eval              AS TABLE fin_opp_eval;
CREATE TABLE arch_e0.fin_strategy_hearing       AS TABLE fin_strategy_hearing;
CREATE TABLE arch_e0.fin_symdig_findings       AS TABLE fin_symdig_findings;
CREATE TABLE arch_e0.fin_venue_io              AS TABLE fin_venue_io;
CREATE TABLE arch_e0.income_events             AS TABLE income_events;
CREATE TABLE arch_e0.opportunities             AS TABLE opportunities;
CREATE TABLE arch_e0.position_cashflows        AS TABLE position_cashflows;
CREATE TABLE arch_e0.position_daily_income     AS TABLE position_daily_income;
CREATE TABLE arch_e0.position_evals            AS TABLE position_evals;
CREATE TABLE arch_e0.position_legs             AS TABLE position_legs;
CREATE TABLE arch_e0.position_reviews          AS TABLE position_reviews;
CREATE TABLE arch_e0.position_settlements      AS TABLE position_settlements;
CREATE TABLE arch_e0.position_swap_snapshots   AS TABLE position_swap_snapshots;
CREATE TABLE arch_e0.positions                 AS TABLE positions;
CREATE TABLE arch_e0.verification_stats        AS TABLE verification_stats;

-- fin_position_canonicals（名义留活清单成员）FK→positions 且 6/6 行全指向待
-- 归档 positions（SG 实拍）——TRUNCATE 按约束存在拦截与行数无关，唯归档全表
-- 并随大集同截方过；新仓落行继续写不受影响（语义=纯 positions 联结映射表）。
CREATE TABLE arch_e0.fin_position_canonicals AS TABLE fin_position_canonicals;

-- 单语句 TRUNCATE：positions↔legs/settlements/cashflows 等互 FK 同事务内消解；
-- 留活表若有 FK 指向本集 → 本语句报错=事务回滚（禁线安全阀，禁加 CASCADE）。
TRUNCATE
	alerts, autotrade_logs, carry_graduations, cex_private_events,
	combo_daily_rollup, execution_control_events, fin_cex_dci_quote,
	fin_position_canonicals,
	fin_cex_events, fin_cex_radar, fin_dept_reports, fin_llm_call,
	fin_opp_eval, fin_strategy_hearing, fin_symdig_findings, fin_venue_io, income_events,
	opportunities, position_cashflows, position_daily_income,
	position_evals, position_legs, position_reviews,
	position_settlements, position_swap_snapshots, positions,
	verification_stats
RESTART IDENTITY;

-- ============ ④ 终态行归档集（在途行留活；DELETE 不归 identity 序列）============
-- 谓词=正向终态枚举（白名单式，未枚举态一律留活——宁可多留不可误归）。

-- paper_spot_carry：SG 实拍域 {active:36, exited:29709}——active=在途留活。
CREATE TABLE arch_e0.paper_spot_carry AS
	SELECT * FROM paper_spot_carry WHERE status = 'exited';
DELETE FROM paper_spot_carry WHERE status = 'exited';

-- explorer_proposals：实拍 {new:40, rejected:6}——new=在途留活（票面全表集转此）。
CREATE TABLE arch_e0.explorer_proposals AS
	SELECT * FROM explorer_proposals WHERE status <> 'new';
DELETE FROM explorer_proposals WHERE status <> 'new';

-- fin_proposal：实拍 {proposed:5, executed:2, expired:5, deferred:1}——
-- proposed/deferred=在途留活（deferred 语义=挂起可复燃，歧义向留活裁）。
-- 被 fin_dci_order（留活账本）引用的提案行同样留活——FK NO ACTION 不硬删。
CREATE TABLE arch_e0.fin_proposal AS
	SELECT * FROM fin_proposal WHERE status IN ('executed', 'expired', 'rejected', 'closed', 'waived')
		AND id NOT IN (SELECT proposal_id FROM fin_dci_order WHERE proposal_id IS NOT NULL);
DELETE FROM fin_proposal WHERE status IN ('executed', 'expired', 'rejected', 'closed', 'waived')
	AND id NOT IN (SELECT proposal_id FROM fin_dci_order WHERE proposal_id IS NOT NULL);

-- fin_trx_vote_rebalance：实拍 {pending:427, executed:119}——pending=在途留活。
CREATE TABLE arch_e0.fin_trx_vote_rebalance AS
	SELECT * FROM fin_trx_vote_rebalance WHERE status = 'executed';
DELETE FROM fin_trx_vote_rebalance WHERE status = 'executed';

-- ============ ⑤ fin_nav_daily 特例：归档全部+最新行回插 ============
-- NAV 连续锚=30d 回撤硬线基线——空表会让回撤分母断层，最新行必须留活。
CREATE TABLE arch_e0.fin_nav_daily AS TABLE fin_nav_daily;
TRUNCATE fin_nav_daily;
INSERT INTO fin_nav_daily
	SELECT * FROM arch_e0.fin_nav_daily ORDER BY day_start DESC LIMIT 1;

COMMIT;

-- ============ 验收：arch vs live 行数逐表对拍（执行日志存证）============
SELECT 'alerts' t, (SELECT count(*) FROM arch_e0.alerts) arch, (SELECT count(*) FROM alerts) live
UNION ALL SELECT 'autotrade_logs', (SELECT count(*) FROM arch_e0.autotrade_logs), (SELECT count(*) FROM autotrade_logs)
UNION ALL SELECT 'carry_graduations', (SELECT count(*) FROM arch_e0.carry_graduations), (SELECT count(*) FROM carry_graduations)
UNION ALL SELECT 'cex_private_events', (SELECT count(*) FROM arch_e0.cex_private_events), (SELECT count(*) FROM cex_private_events)
UNION ALL SELECT 'combo_daily_rollup', (SELECT count(*) FROM arch_e0.combo_daily_rollup), (SELECT count(*) FROM combo_daily_rollup)
UNION ALL SELECT 'execution_control_events', (SELECT count(*) FROM arch_e0.execution_control_events), (SELECT count(*) FROM execution_control_events)
UNION ALL SELECT 'fin_cex_dci_quote', (SELECT count(*) FROM arch_e0.fin_cex_dci_quote), (SELECT count(*) FROM fin_cex_dci_quote)
UNION ALL SELECT 'fin_cex_events', (SELECT count(*) FROM arch_e0.fin_cex_events), (SELECT count(*) FROM fin_cex_events)
UNION ALL SELECT 'fin_cex_radar', (SELECT count(*) FROM arch_e0.fin_cex_radar), (SELECT count(*) FROM fin_cex_radar)
UNION ALL SELECT 'fin_dept_reports', (SELECT count(*) FROM arch_e0.fin_dept_reports), (SELECT count(*) FROM fin_dept_reports)
UNION ALL SELECT 'fin_llm_call', (SELECT count(*) FROM arch_e0.fin_llm_call), (SELECT count(*) FROM fin_llm_call)
UNION ALL SELECT 'fin_opp_eval', (SELECT count(*) FROM arch_e0.fin_opp_eval), (SELECT count(*) FROM fin_opp_eval)
UNION ALL SELECT 'fin_symdig_findings', (SELECT count(*) FROM arch_e0.fin_symdig_findings), (SELECT count(*) FROM fin_symdig_findings)
UNION ALL SELECT 'fin_venue_io', (SELECT count(*) FROM arch_e0.fin_venue_io), (SELECT count(*) FROM fin_venue_io)
UNION ALL SELECT 'income_events', (SELECT count(*) FROM arch_e0.income_events), (SELECT count(*) FROM income_events)
UNION ALL SELECT 'opportunities', (SELECT count(*) FROM arch_e0.opportunities), (SELECT count(*) FROM opportunities)
UNION ALL SELECT 'position_cashflows', (SELECT count(*) FROM arch_e0.position_cashflows), (SELECT count(*) FROM position_cashflows)
UNION ALL SELECT 'position_daily_income', (SELECT count(*) FROM arch_e0.position_daily_income), (SELECT count(*) FROM position_daily_income)
UNION ALL SELECT 'position_evals', (SELECT count(*) FROM arch_e0.position_evals), (SELECT count(*) FROM position_evals)
UNION ALL SELECT 'position_legs', (SELECT count(*) FROM arch_e0.position_legs), (SELECT count(*) FROM position_legs)
UNION ALL SELECT 'position_reviews', (SELECT count(*) FROM arch_e0.position_reviews), (SELECT count(*) FROM position_reviews)
UNION ALL SELECT 'position_settlements', (SELECT count(*) FROM arch_e0.position_settlements), (SELECT count(*) FROM position_settlements)
UNION ALL SELECT 'position_swap_snapshots', (SELECT count(*) FROM arch_e0.position_swap_snapshots), (SELECT count(*) FROM position_swap_snapshots)
UNION ALL SELECT 'positions', (SELECT count(*) FROM arch_e0.positions), (SELECT count(*) FROM positions)
UNION ALL SELECT 'verification_stats', (SELECT count(*) FROM arch_e0.verification_stats), (SELECT count(*) FROM verification_stats)
UNION ALL SELECT 'paper_spot_carry', (SELECT count(*) FROM arch_e0.paper_spot_carry), (SELECT count(*) FROM paper_spot_carry)
UNION ALL SELECT 'explorer_proposals', (SELECT count(*) FROM arch_e0.explorer_proposals), (SELECT count(*) FROM explorer_proposals)
UNION ALL SELECT 'fin_strategy_hearing', (SELECT count(*) FROM arch_e0.fin_strategy_hearing), (SELECT count(*) FROM fin_strategy_hearing)
UNION ALL SELECT 'fin_position_canonicals', (SELECT count(*) FROM arch_e0.fin_position_canonicals), (SELECT count(*) FROM fin_position_canonicals)
UNION ALL SELECT 'fin_proposal', (SELECT count(*) FROM arch_e0.fin_proposal), (SELECT count(*) FROM fin_proposal)
UNION ALL SELECT 'fin_trx_vote_rebalance', (SELECT count(*) FROM arch_e0.fin_trx_vote_rebalance), (SELECT count(*) FROM fin_trx_vote_rebalance)
UNION ALL SELECT 'fin_nav_daily', (SELECT count(*) FROM arch_e0.fin_nav_daily), (SELECT count(*) FROM fin_nav_daily)
ORDER BY 1;

-- pre-flight FK 查询（执行前手工跑，应零行——有行=留活表引用归档表，先裁后跑）：
--   SELECT conrelid::regclass live_table, confrelid::regclass arch_table, conname
--   FROM pg_constraint WHERE contype='f' AND confrelid::regclass::text IN
--   ('alerts','autotrade_logs','carry_graduations','cex_private_events','combo_daily_rollup',
--    'execution_control_events','fin_cex_dci_quote','fin_cex_events','fin_cex_radar',
--    'fin_dept_reports','fin_llm_call','fin_opp_eval','fin_symdig_findings','fin_venue_io',
--    'income_events','opportunities','position_cashflows','position_daily_income',
--    'position_evals','position_legs','position_reviews','position_settlements',
--    'position_swap_snapshots','positions','verification_stats')
--   AND conrelid::regclass::text NOT IN (同上清单);
