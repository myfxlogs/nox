package oppmatch

import (
	"context"
	"strings"
	"testing"

	"nox/internal/platform/testutil"
	"nox/internal/store/pgstore"
)

// TestD187_RunOnceRealStore：真测试库首扫——≥1 提案或缺口（票面验收同型），
// 二轮幂等（缺口已 requested 零再推）。
func TestD187_RunOnceRealStore(t *testing.T) {
	dsn := testutil.TestDSN(t, "NOX_TEST_PG_DSN")
	st, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := RunOnce(ctx, st, 10000, 1000); err != nil {
		t.Fatal(err)
	}
	// 累计面≥1（测试库跨轮残留使"首扫"非首轮——断言总效果非本轮回传）。
	// D-247 预检后产出面=3 通道：缺口行+提案告警+休眠约束（冷库全能力缺失时
	// 前两通道正当为零——休眠即产出）。
	var gapRows, propAlerts, dormantRows int
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM fin_condition_gap`).Scan(&gapRows); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM alerts WHERE message LIKE '机会提案%'`).Scan(&propAlerts); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM fin_capability_constraint WHERE subject LIKE 'dormant:%'`).Scan(&dormantRows); err != nil {
		t.Fatal(err)
	}
	if gapRows+propAlerts+dormantRows == 0 {
		t.Fatal("匹配零产出=静默病灶（缺口行+提案告警+休眠约束皆无）")
	}
	_, g2, err := RunOnce(ctx, st, 10000, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if g2 != 0 {
		t.Fatalf("二轮缺口应幂等零新请求, got %d", g2)
	}
}

// TestD189_RunOnceProposalRows：提案实体化——稳定 id 告警+同模板不重开+过期并入扫描拍。
func TestD189_RunOnceProposalRows(t *testing.T) {
	dsn := testutil.TestDSN(t, "NOX_TEST_PG_DSN")
	st, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `DELETE FROM fin_proposal; DELETE FROM alerts`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := RunOnce(ctx, st, 10000, 1000); err != nil {
		t.Fatal(err)
	}
	// 提案行数==告警数且告警携稳定 id（提案#N 形）。
	var rows, alerts int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM fin_proposal`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM alerts WHERE message ~ '机会提案#[0-9]+'`).Scan(&alerts); err != nil {
		t.Fatal(err)
	}
	if rows == 0 {
		t.Skip("无达标模板——提案面零产出环境")
	}
	if alerts == 0 {
		t.Fatal("提案告警未携稳定数据库 id（提案#N 形）")
	}
	// 二扫：同模板 proposed 不重开行（幂等真锚）。
	if _, _, err := RunOnce(ctx, st, 10000, 1000); err != nil {
		t.Fatal(err)
	}
	var rows2 int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM fin_proposal`).Scan(&rows2); err != nil {
		t.Fatal(err)
	}
	if rows2 != rows {
		t.Fatalf("二扫提案行应幂等: %d→%d", rows, rows2)
	}
	// 过期并入扫描拍——全模板置不可行（live 集空）→pending 全 expired。
	if n, err := st.ExpireProposalsExcept(ctx, nil); err != nil || n != rows {
		t.Fatalf("expire 并入拍 n=%d want %d: %v", n, rows, err)
	}
}

// TestD193_Sentinel：venue 悬空自检+缺口自动收口/复开（真库扫描拍径）。
func TestD193_Sentinel(t *testing.T) {
	dsn := testutil.TestDSN(t, "NOX_TEST_PG_DSN")
	st, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx,
		`DELETE FROM fin_condition_gap; DELETE FROM alerts;
		 DELETE FROM fin_opp_template WHERE id LIKE 't193%'`); err != nil {
		t.Fatal(err)
	}
	// 三枚裁判模板：t193_ok 满足/holding=0、t193_miss 恒缺、t193_unk 权限未知。
	if _, err := st.Pool().Exec(ctx, `
		INSERT INTO fin_opp_template (id, mechanism, net_apr, exec_track, risk_level, enabled, payoff_class) VALUES
		('t193_ok','ok','x','proposal','low',true,'other'),('t193_miss','miss','x','proposal','low',true,'other'),
		('t193_unk','unk','x','proposal','low',true,'other');
		INSERT INTO fin_opp_template_cond (template_id, seq, kind, subject, qualifier, min_val) VALUES
		('t193_ok',1,'holding','BTC','min_qty',0),
		('t193_miss',1,'holding','ZZZ','min_qty',999),
		('t193_unk',1,'permission','probe_x','api',NULL)`); err != nil {
		t.Fatal(err)
	}
	mkGap := func(tid, kind, subj, status string) int64 {
		var id int64
		if err := st.Pool().QueryRow(ctx,
			`INSERT INTO fin_condition_gap (template_id,kind,subject,request_text,status)
			 VALUES ($1,$2,$3,'r',$4) RETURNING id`, tid, kind, subj, status).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	gOK := mkGap("t193_ok", "holding", "BTC", "requested")          // →fulfilled
	gStale := mkGap("t193_ok", "credential", "gone:x", "requested") // →fulfilled+需求已变更
	gMiss := mkGap("t193_miss", "holding", "ZZZ", "fulfilled")      // →requested 复开
	gUnk := mkGap("t193_unk", "permission", "probe_x", "requested") // →unknown 不收
	gRT := mkGap("t193_ok", "holding", "dual_order", "requested")   // 条件行外 subject→需求已变更收口
	if _, _, err := RunOnce(ctx, st, 10000, 1000); err != nil {
		t.Fatal(err)
	}
	stOf := func(id int64) (string, *string) {
		var stt string
		var nn *string
		if err := st.Pool().QueryRow(ctx,
			`SELECT status, resolve_note FROM fin_condition_gap WHERE id=$1`, id).
			Scan(&stt, &nn); err != nil {
			t.Fatal(err)
		}
		return stt, nn
	}
	if stt, _ := stOf(gOK); stt != "fulfilled" {
		t.Fatalf("满足条件应自动收口: %s", stt)
	}
	if stt, nn := stOf(gStale); stt != "fulfilled" || nn == nil || *nn != "需求已变更" {
		t.Fatalf("条件行删除应收口+note: %s %v", stt, nn)
	}
	if stt, _ := stOf(gMiss); stt != "requested" {
		t.Fatalf("fulfilled 行条件再失应复开: %s", stt)
	}
	if stt, _ := stOf(gUnk); stt != "requested" {
		t.Fatalf("unknown 态不收不复开: %s", stt)
	}
	if stt, nn := stOf(gRT); stt != "fulfilled" || nn == nil || *nn != "需求已变更" {
		t.Fatalf("条件外 subject 应收口: %s %v", stt, nn)
	}
	// venue 悬空：dci_buy_low 声明 gate:dual vs executor=binance→warn 行；
	// 未接执行轨模板（option_spread 等）不报。
	var n int
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM alerts WHERE message LIKE '模板venue悬空%dci_buy_low%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("venue 悬空未报")
	}
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM alerts WHERE message LIKE '模板venue悬空%option_spread%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("未接执行轨模板误报悬空")
	}
}

// TestD193_EvalCondTri 三态求值纯锚：permission 行缺/unknown→unknown。
func TestD193_EvalCondTri(t *testing.T) {
	v := LedgerView{PermStates: map[string]string{"p_ok": "present", "p_no": "absent", "p_uk": "unknown"}}
	if EvalCondTri(v, Cond{Kind: "permission", Subject: "p_ok"}) != "ok" ||
		EvalCondTri(v, Cond{Kind: "permission", Subject: "p_no"}) != "miss" ||
		EvalCondTri(v, Cond{Kind: "permission", Subject: "p_uk"}) != "unknown" ||
		EvalCondTri(v, Cond{Kind: "permission", Subject: "p_absent_row"}) != "unknown" {
		t.Fatal("permission 三态错")
	}
	if EvalCondTri(v, Cond{Kind: "holding", Subject: "BTC", MinVal: nil}) != "miss" {
		t.Fatal("确定性二态错")
	}
}

// TestD204_DormantTrack：休眠轨锚——dormant 约束行在账→扫描零新提案+存量 proposed 翻 deferred。
// （迁移 0199 种子 covered_call@binance 休眠行随 boot 落库——真账口径锚。）
func TestD204_DormantTrack(t *testing.T) {
	dsn := testutil.TestDSN(t, "NOX_TEST_PG_DSN")
	st, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `DELETE FROM fin_proposal; DELETE FROM alerts`); err != nil {
		t.Fatal(err)
	}
	// 存量 proposed 行（休眠轨）→应翻 deferred。
	if _, err := st.Pool().Exec(ctx, `INSERT INTO fin_proposal
		(template_id, exec_track, venue, params, expected_apr, status)
		VALUES ('dci_sell_high_covered_call','dci','binance','{}',0.1,'proposed')`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := RunOnce(ctx, st, 10000, 1000); err != nil {
		t.Fatal(err)
	}
	var st_ string
	if err := st.Pool().QueryRow(ctx,
		`SELECT status FROM fin_proposal WHERE template_id='dci_sell_high_covered_call'`).Scan(&st_); err != nil {
		t.Fatal(err)
	}
	if st_ != "deferred" {
		t.Fatalf("休眠轨存量行未翻 deferred：%s", st_)
	}
	// 休眠轨零新提案（同模板仅 1 行——种子行使该模板在休眠集）。
	var n int
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM fin_proposal WHERE template_id='dci_sell_high_covered_call'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("休眠轨产新提案：%d 行", n)
	}
}

// TestZombieProposalSemantics auto 轨僵尸提案处置两径锚：
// ①auto 轨+无执行器模板 → 零新提案+存量 proposed 翻 deferred+info 留痕；
// ②unevaluable 连拍≥3 → deferred（评估证据持续不足不许无限挂 proposed）。
func TestZombieProposalSemantics(t *testing.T) {
	dsn := testutil.TestDSN(t, "NOX_TEST_PG_DSN")
	st, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx,
		`DELETE FROM fin_proposal; DELETE FROM alerts;
		 DELETE FROM fin_opp_eval;
		 DELETE FROM fin_opp_template_cond WHERE template_id LIKE 'tzombie%';
		 DELETE FROM fin_opp_template WHERE id LIKE 'tzombie%'`); err != nil {
		t.Fatal(err)
	}
	// A：auto 轨无执行器（tzombie_auto 不在 IsExecTemplate 族）；holding BTC min 0=恒满足。
	// B：proposal 轨（不触①），tid 无证据源 → 评估卡 unevaluable 连拍。
	if _, err := st.Pool().Exec(ctx, `
		INSERT INTO fin_opp_template (id, mechanism, net_apr, exec_track, risk_level, enabled, payoff_class) VALUES
		('tzombie_auto','x','x','auto','low',true,'other'),('tzombie_eval','x','x','proposal','low',true,'other');
		INSERT INTO fin_opp_template_cond (template_id, seq, kind, subject, qualifier, min_val) VALUES
		('tzombie_auto',1,'holding','BTC','min_qty',0),
		('tzombie_eval',1,'holding','BTC','min_qty',0)`); err != nil {
		t.Fatal(err)
	}
	// A 存量僵尸行（#221/#225 同型）。
	if _, err := st.Pool().Exec(ctx, `INSERT INTO fin_proposal
		(template_id, exec_track, venue, params, status)
		VALUES ('tzombie_auto','auto','binance','{}','proposed')`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := RunOnce(ctx, st, 10000, 1000); err != nil {
		t.Fatal(err)
	}
	var stt string
	if err := st.Pool().QueryRow(ctx,
		`SELECT status FROM fin_proposal WHERE template_id='tzombie_auto'`).Scan(&stt); err != nil {
		t.Fatal(err)
	}
	if stt != "deferred" {
		t.Fatalf("auto 无执行器存量应翻 deferred, got %s", stt)
	}
	var alerts int
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM alerts WHERE message LIKE 'auto 轨无执行器%tzombie_auto%'`).Scan(&alerts); err != nil {
		t.Fatal(err)
	}
	if alerts == 0 {
		t.Fatal("executor-missing info 留痕缺")
	}
	// A 零新提案（行数仍 1 且非 proposed）。
	var n int
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM fin_proposal WHERE template_id='tzombie_auto' AND status='proposed'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("auto 无执行器不应产/留 proposed 行")
	}
	// B：连拍 unevaluable×3 → deferred。
	for i := 0; i < 3; i++ {
		if _, _, err := RunOnce(ctx, st, 10000, 1000); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Pool().QueryRow(ctx,
		`SELECT status FROM fin_proposal WHERE template_id='tzombie_eval'`).Scan(&stt); err != nil {
		t.Fatal(err)
	}
	if stt != "deferred" {
		t.Fatalf("unevaluable 连拍≥3 应翻 deferred, got %s", stt)
	}
}

// TestXferScan 跨 venue 机会差调拨提案锚：同资产 earn APR 差超阈+源池闲量
// →fin_condition_gap 投资人侧请求行；重扫幂等零重推。
func TestXferScan(t *testing.T) {
	dsn := testutil.TestDSN(t, "NOX_TEST_PG_DSN")
	st, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	// 种子：binance 闲 USDT 池（零收入=Idle 标）+两所 earn APR（差 7pt>阈 5）。
	if _, err := st.Pool().Exec(ctx, `
		DELETE FROM fin_venue_account_snapshot WHERE venue='xferbin';
		DELETE FROM fin_cex_earn_apr WHERE venue IN ('xferbin','xferokx');
		DELETE FROM fin_condition_gap WHERE template_id='cross_venue_xfer'`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `
		INSERT INTO fin_venue_account_snapshot
			(venue, venue_type, balance_raw, equity_raw, free_margin_raw, currency, fetched_at)
			VALUES ('xferbin','spot',1000,1000,1000,'USDT',now());
		INSERT INTO fin_cex_earn_apr (venue, ccy, product_type, apr) VALUES
			('xferbin','USDT','flexible',0.02),('xferokx','USDT','flexible',0.09)`); err != nil {
		t.Fatal(err)
	}
	xferScan(ctx, st)
	var n int
	var txt string
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*), max(request_text) FROM fin_condition_gap
		 WHERE template_id='cross_venue_xfer' AND subject='USDT:xferbin>xferokx'`).Scan(&n, &txt); err != nil {
		t.Fatal(err)
	}
	if n != 1 || !strings.Contains(txt, "xferbin→xferokx") {
		t.Fatalf("调拨请求行缺/文案错: n=%d txt=%q", n, txt)
	}
	var status string
	if err := st.Pool().QueryRow(ctx,
		`SELECT status FROM fin_condition_gap WHERE template_id='cross_venue_xfer' AND subject='USDT:xferbin>xferokx'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "requested" {
		t.Fatalf("应 requested（已推业主）: %s", status)
	}
	// 重扫幂等——行数不变。
	xferScan(ctx, st)
	var n2 int
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM fin_condition_gap WHERE template_id='cross_venue_xfer' AND subject='USDT:xferbin>xferokx'`).Scan(&n2); err != nil {
		t.Fatal(err)
	}
	if n2 != 1 {
		t.Fatalf("重扫应幂等零重推: %d", n2)
	}
	// 反向差（xferokx→xferbin）不应产行。
	var rev int
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM fin_condition_gap WHERE subject LIKE 'USDT:xferokx%'`).Scan(&rev); err != nil {
		t.Fatal(err)
	}
	if rev != 0 {
		t.Fatal("反向差不应产提案")
	}
}

// TestXferScan_EarnPoolSource D-612 低效仓门锚：earn 已部署仓入源域——
// xfergate:earn 仓权益+差超阈→调拨提案行产，文案带集中度透出（补正③ 35% 读数）。
func TestXferScan_EarnPoolSource(t *testing.T) {
	dsn := testutil.TestDSN(t, "NOX_TEST_PG_DSN")
	st, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	// 共享库防串扰：earn APR 全清（dst=argmax 会被其他测试残行抢位）。
	if _, err := st.Pool().Exec(ctx, `
		DELETE FROM fin_venue_account_snapshot WHERE venue IN ('xfrgate','xfrdst');
		DELETE FROM fin_cex_earn_apr;
		DELETE FROM fin_condition_gap WHERE subject LIKE 'USDT:xfrgate%'`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `
		INSERT INTO fin_venue_account_snapshot
			(venue, venue_type, balance_raw, equity_raw, free_margin_raw, currency, fetched_at)
			VALUES ('xfrgate','earn',800,800,0,'USDT',now());
		INSERT INTO fin_cex_earn_apr (venue, ccy, product_type, apr) VALUES
			('xfrgate','USDT','flexible',0.02),('xfrdst','USDT','flexible',0.09)`); err != nil {
		t.Fatal(err)
	}
	xferScan(ctx, st)
	var txt string
	if err := st.Pool().QueryRow(ctx,
		`SELECT request_text FROM fin_condition_gap
		 WHERE template_id='cross_venue_xfer' AND subject='USDT:xfrgate>xfrdst'`).Scan(&txt); err != nil {
		t.Fatalf("earn 仓源调拨提案缺: %v", err)
	}
	if !strings.Contains(txt, "xfrgate→xfrdst") || !strings.Contains(txt, "集中度") {
		t.Fatalf("提案文案缺集中度透出: %q", txt)
	}
}

// TestRotateScan_Proposal D-612 所内面锚：earn 池权益≥阈+鲜 APR 行→
// earn_rotation 提案+评估卡落（净值差终判在执行拍实拉——扫描只产候选）。
func TestRotateScan_Proposal(t *testing.T) {
	dsn := testutil.TestDSN(t, "NOX_TEST_PG_DSN")
	st, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `
		DELETE FROM fin_venue_account_snapshot WHERE venue IN ('okx','gate');
		DELETE FROM fin_cex_earn_apr WHERE venue IN ('okx','gate');
		DELETE FROM fin_proposal WHERE template_id='earn_rotation' AND venue='okx'`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `
		INSERT INTO fin_venue_account_snapshot
			(venue, venue_type, balance_raw, equity_raw, free_margin_raw, currency, fetched_at)
			VALUES ('okx','earn',800,800,0,'USDT',now());
		INSERT INTO fin_cex_earn_apr (venue, ccy, product_type, apr) VALUES
			('okx','USDT','flexible',0.036)`); err != nil {
		t.Fatal(err)
	}
	rotateScan(ctx, st)
	var n int
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM fin_proposal WHERE template_id='earn_rotation' AND venue='okx' AND status='proposed'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("earn_rotation 提案缺: %d", n)
	}
	// 提案须带评估卡（无卡拒单 M4.1——autoexec 消费前提）。
	if err := st.Pool().QueryRow(ctx, `
		SELECT count(*) FROM fin_opp_eval e JOIN fin_proposal p ON e.proposal_id=p.id
		WHERE p.template_id='earn_rotation' AND p.venue='okx'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("earn_rotation 评估卡缺: %d", n)
	}
	// 重扫幂等（proposed 唯一锚）。
	rotateScan(ctx, st)
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM fin_proposal WHERE template_id='earn_rotation' AND venue='okx' AND status='proposed'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("重扫应幂等: %d", n)
	}
	// 陈 APR 行不比（fail-closed）：另一 venue 只落 25h 前行 → 零提案。
	if _, err := st.Pool().Exec(ctx, `
		INSERT INTO fin_venue_account_snapshot
			(venue, venue_type, balance_raw, equity_raw, free_margin_raw, currency, fetched_at)
			VALUES ('gate','earn',800,800,0,'USDT',now());
		INSERT INTO fin_cex_earn_apr (venue, ccy, product_type, apr, ts) VALUES
			('gate','USDT','flexible',0.036,now()-interval '25 hours')`); err != nil {
		t.Fatal(err)
	}
	rotateScan(ctx, st)
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM fin_proposal WHERE template_id='earn_rotation' AND venue='gate'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("陈 APR 行不应产提案: %d", n)
	}
}
