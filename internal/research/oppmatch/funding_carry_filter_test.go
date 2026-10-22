// funding_carry_filter_test.go —— #1947 同伤修三断锚：venue 过滤/极端值剔/
// 陈帧剔（gross=1414.29% 伪 APR 实证面——异所/超物理界/死流 funding 行不入围）。
package oppmatch

import (
	"context"
	"testing"
	"time"

	"nox/internal/platform/testutil"
	"nox/internal/store/pgstore"
)

func TestFundingCarryBest_Filters(t *testing.T) {
	dsn := testutil.TestDSN(t, "NOX_TEST_PG_DSN")
	st, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now := time.Now()
	rows := []struct {
		venue, sym string
		val        float64
		age        time.Duration
	}{
		{"probe_fc", "GOOD", 0.35, time.Hour},       // 合格——应中
		{"probe_fc", "EXTREME", 14.1429, time.Hour}, // >cap 极端值——剔（#1947 本尊形）
		{"probe_fc", "STALE", 0.9, 96 * time.Hour},  // 陈帧——剔
		{"probe_other", "HIGHER", 0.8, time.Hour},   // 异所更高——不串
	}
	for _, r := range rows {
		if _, err := st.Pool().Exec(ctx, `INSERT INTO facts (kind,venue,symbol,value,unit,ts,src)
			VALUES ('funding',$1,$2,$3,'pct_annualized',$4,'probe')`,
			r.venue, r.sym, r.val, now.Add(-r.age)); err != nil {
			t.Fatalf("fact 插行 %s: %v", r.sym, err)
		}
	}
	defer func() {
		_, _ = st.Pool().Exec(ctx,
			`DELETE FROM facts WHERE src='probe' AND venue IN ('probe_fc','probe_other')`)
	}()
	best, err := fundingCarryBest(ctx, st, "probe_fc")
	if err != nil {
		t.Fatal(err)
	}
	if best == nil {
		t.Fatal("合格行在册应回最优")
	}
	if best.Symbol != "GOOD" || best.Value != 0.35 {
		t.Fatalf("最优行应=GOOD（极端值/陈帧/异所全剔），得 %s %.4f", best.Symbol, best.Value)
	}
	// 全剔面——异所域查询应无合格行（诚实缺非串取）。
	b2, err := fundingCarryBest(ctx, st, "probe_none")
	if err != nil || b2 != nil {
		t.Fatalf("无合格 venue 应 nil: %v %v", b2, err)
	}
}
