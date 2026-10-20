package oppmatch

import (
	"testing"
)

// TestEarnRailVenues_IdleProposal earn 多 venue 化锚：earnExecVenues 面内闲池
// （okx/gate）→cash_mgmt 提案 venue 键分行零缺口行；<$100 尘池静默；
// 白名单外 venue 照旧 executor 缺口。
func TestEarnRailVenues_IdleProposal(t *testing.T) {
	st, ctx := d514Store(t)
	for _, q := range []string{
		`DELETE FROM fin_proposal WHERE venue IN ('d514okx','okx','gate','xgnovenue')`,
		`DELETE FROM fin_condition_gap WHERE subject LIKE 'okx%' OR subject LIKE 'gate%' OR subject LIKE 'xgnovenue%'`,
	} {
		if _, err := st.Pool().Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO fin_cex_earn_apr
		(venue, ccy, product_type, apr, ts) VALUES
		('okx','USDT','flex',0.031,now()),('gate','USDT','flex',0.025,now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO fin_venue_account_snapshot
		(venue, venue_type, currency, balance_raw, equity_raw, fetched_at, src) VALUES
		('okx','unified','USDT',1956,1956,now(),'ws'),
		('gate','perp','USDT',998,998,now(),'ws'),
		('okx','earn','USDT',90,90,now(),'ws'),
		('xgnovenue','spot','USDT',500,500,now(),'ws')`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = st.Pool().Exec(ctx,
			`DELETE FROM fin_venue_account_snapshot WHERE venue IN ('okx','gate','xgnovenue')`)
		_, _ = st.Pool().Exec(ctx,
			`DELETE FROM fin_proposal WHERE template_id='cash_mgmt' AND venue IN ('okx','gate')`)
		_, _ = st.Pool().Exec(ctx,
			`DELETE FROM fin_condition_gap WHERE subject LIKE 'xgnovenue%'`)
		_, _ = st.Pool().Exec(ctx,
			`DELETE FROM fin_cex_earn_apr WHERE venue IN ('okx','gate') AND product_type='flex'`)
		_, _ = st.Pool().Exec(ctx,
			`DELETE FROM fin_opp_eval WHERE proposal_id IN
			 (SELECT id FROM fin_proposal WHERE template_id='cash_mgmt' AND venue IN ('okx','gate'))`)
	}()
	idleGapScan(ctx, st)
	var okxP, gateP, dustP, gapN, xgap int
	for _, q := range []struct {
		dst *int
		sql string
	}{
		{&okxP, `SELECT count(*) FROM fin_proposal WHERE template_id='cash_mgmt' AND venue='okx' AND status='proposed'`},
		{&gateP, `SELECT count(*) FROM fin_proposal WHERE template_id='cash_mgmt' AND venue='gate' AND status='proposed'`},
		{&dustP, `SELECT count(*) FROM fin_proposal WHERE template_id='cash_mgmt' AND venue='okx' AND params::text LIKE '%earn%'`},
		{&gapN, `SELECT count(*) FROM fin_condition_gap WHERE kind='executor' AND subject IN ('okx:unified','gate:perp')`},
		{&xgap, `SELECT count(*) FROM fin_condition_gap WHERE kind='executor' AND subject='xgnovenue:spot'`},
	} {
		if err := st.Pool().QueryRow(ctx, q.sql).Scan(q.dst); err != nil {
			t.Fatal(err)
		}
	}
	if okxP != 1 || gateP != 1 {
		t.Fatalf("rail 面内闲池应产提案: okx=%d gate=%d", okxP, gateP)
	}
	if gapN != 0 {
		t.Fatalf("rail 面内 venue 不应再出 executor 缺口: %d", gapN)
	}
	if xgap != 1 {
		t.Fatalf("白名单外 venue 缺口照旧: %d", xgap)
	}
	// 评估卡同步产（M4.1 无卡拒单返修锚）：okx/gate 提案各一卡 verdict 非空。
	var cards int
	if err := st.Pool().QueryRow(ctx, `
		SELECT count(*) FROM fin_opp_eval e JOIN fin_proposal p ON e.proposal_id=p.id
		 WHERE p.template_id='cash_mgmt' AND p.venue IN ('okx','gate') AND e.verdict<>''`).Scan(&cards); err != nil {
		t.Fatal(err)
	}
	if cards != 2 {
		t.Fatalf("提案无卡: %d", cards)
	}
	var gross float64
	if err := st.Pool().QueryRow(ctx, `
		SELECT e.gross_apr FROM fin_opp_eval e JOIN fin_proposal p ON e.proposal_id=p.id
		 WHERE p.venue='okx'`).Scan(&gross); err != nil {
		t.Fatal(err)
	}
	if gross <= 0 || gross > 1 {
		t.Fatalf("venue APR 证源错: %v", gross)
	}
	// 幂等重跑零重产。
	idleGapScan(ctx, st)
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM fin_proposal WHERE template_id='cash_mgmt' AND venue='okx' AND status='proposed'`).Scan(&okxP); err != nil {
		t.Fatal(err)
	}
	if okxP != 1 {
		t.Fatalf("重跑重产: %d", okxP)
	}
}

// D-523⑥+expire 粘性修：cash_mgmt 提案目标款消失→miss_count 累积连 3 拍
// miss 才 expired（单拍瞬态 miss 不再判死——491 病灶修法语义锚）。
func TestCashMgmtTargetGone_ExpiresInScan(t *testing.T) {
	st, ctx := d514Store(t)
	for _, q := range []string{
		`DELETE FROM fin_proposal WHERE venue='xgdead'`,
	} {
		if _, err := st.Pool().Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		_, _ = st.Pool().Exec(ctx, `DELETE FROM fin_proposal WHERE venue='xgdead'`)
	}()
	// 挂一条 xgdead venue 的 proposed cash_mgmt（该 venue 无闲池=目标款消失）。
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO fin_proposal (template_id, exec_track, venue, status, created_at)
		 VALUES ('cash_mgmt','auto','xgdead','proposed',now())`); err != nil {
		t.Fatal(err)
	}
	// 拍1：单拍 miss=存活（miss_count=1——粘性修语义锚）
	idleGapScan(ctx, st)
	var st1, mc1 string
	if err := st.Pool().QueryRow(ctx,
		`SELECT status, miss_count::text FROM fin_proposal WHERE venue='xgdead' AND template_id='cash_mgmt'`).Scan(&st1, &mc1); err != nil {
		t.Fatal(err)
	}
	if st1 != "proposed" || mc1 != "1" {
		t.Fatalf("单拍 miss 应存活 miss_count=1, got %s/%s", st1, mc1)
	}
	// 拍2/3：连 3 拍 miss=expired
	idleGapScan(ctx, st)
	idleGapScan(ctx, st)
	var st2 string
	if err := st.Pool().QueryRow(ctx,
		`SELECT status FROM fin_proposal WHERE venue='xgdead' AND template_id='cash_mgmt'`).Scan(&st2); err != nil {
		t.Fatal(err)
	}
	if st2 != "expired" {
		t.Fatalf("target 消失连 3 拍应 expired, got %s", st2)
	}
}
