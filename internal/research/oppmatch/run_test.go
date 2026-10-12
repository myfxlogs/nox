package oppmatch

import (
	"context"
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
	if _, _, err := RunOnce(ctx, st); err != nil {
		t.Fatal(err)
	}
	// 累计面≥1（测试库跨轮残留使"首扫"非首轮——断言总效果非本轮回传）。
	var gapRows, propAlerts int
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM fin_condition_gap`).Scan(&gapRows); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM alerts WHERE message LIKE '机会提案%'`).Scan(&propAlerts); err != nil {
		t.Fatal(err)
	}
	if gapRows+propAlerts == 0 {
		t.Fatal("匹配零产出=静默病灶（缺口行+提案告警皆无）")
	}
	_, g2, err := RunOnce(ctx, st)
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
	if _, _, err := RunOnce(ctx, st); err != nil {
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
	if _, _, err := RunOnce(ctx, st); err != nil {
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
		INSERT INTO fin_opp_template (id, mechanism, net_apr, exec_track, risk_level, enabled) VALUES
		('t193_ok','ok','x','proposal','low',true),('t193_miss','miss','x','proposal','low',true),
		('t193_unk','unk','x','proposal','low',true);
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
	gRT := mkGap("t193_ok", "runtime", "dual_order", "requested")   // D-196③→豁免零动
	if _, _, err := RunOnce(ctx, st); err != nil {
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
	if stt, nn := stOf(gRT); stt != "requested" || nn != nil {
		t.Fatalf("runtime 行应豁免收口/复开: %s %v", stt, nn)
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
	if _, _, err := RunOnce(ctx, st); err != nil {
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
