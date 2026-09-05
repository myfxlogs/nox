package dashboard

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"arb/internal/store/pgstore"
	"arb/internal/testutil"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
)

// DSN 门控（D-207）；哨兵日期 2098/2099 避免与真实/回填数据撞车（共享库 B-1 债）。
func cnfiSeedSnapshot(t *testing.T) *Service {
	t.Helper()
	dsn := testutil.TestDSN(t, "ARB_TEST_PG_DSN")
	st, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgstore: %v", err)
	}
	t.Cleanup(st.Close)
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// R-F1a（D-241 审计挂账）：共享 arb_test 库有真实回填数据——先清空 cnfi_series
	// 再播种哨兵日，否则快照断言遇既有行假 FAIL（D-212 hedgeTestStore 惯例）。
	if _, err := st.Pool().Exec(ctx, `TRUNCATE cnfi_series`); err != nil {
		t.Fatalf("truncate cnfi_series: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(ctx, `DELETE FROM cnfi_series WHERE as_of >= '2098-12-31'`)
	})
	// 两日数据：2098-12-31（旧）+ 2099-01-01（新=snapshot 锚）。
	// bank_aaa 10y 两日均缺 → missing；credit_spread_3y 两腿新日齐 → 166.0-125.0=41.0bp。
	seed := []struct {
		s, tenor string
		bp       float64
		day      string
	}{
		{"cgb", "2y", 124.0, "2099-01-01"},
		{"cgb", "10y", 168.0, "2099-01-01"},
		{"cgb", "3y", 125.0, "2099-01-01"},
		{"ig_aaa", "3y", 166.0, "2099-01-01"},
		{"cgb", "2y", 123.0, "2098-12-31"},
		{"cgb", "10y", 167.0, "2098-12-31"},
	}
	for _, r := range seed {
		if _, err := st.Pool().Exec(ctx,
			`INSERT INTO cnfi_series (as_of,series,tenor,value_bp,source,source_ref,fetched_at)
			 VALUES ($1,$2,$3,$4,'test','t',now()) ON CONFLICT (as_of,series,tenor) DO NOTHING`,
			r.day, r.s, r.tenor, r.bp); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	return &Service{store: st}
}

func TestGetCNFIRadarSnapshot_Semantics(t *testing.T) {
	s := cnfiSeedSnapshot(t)
	resp, err := s.GetCNFIRadarSnapshot(context.Background(),
		connect.NewRequest(&v1.GetCNFIRadarSnapshotRequest{}))
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	msg := resp.Msg
	if msg.SnapshotAsOf != "2099-01-01" {
		t.Fatalf("snapshot_as_of = %q, want 2099-01-01", msg.SnapshotAsOf)
	}
	getPt := func(series, tenor string) *v1.CnfiCurvePoint {
		for _, p := range msg.Points {
			if p.Series == series && p.Tenor == tenor {
				return p
			}
		}
		return nil
	}
	if p := getPt("cgb", "10y"); p == nil || p.Quality != "ok" || p.ValueBp != 168.0 {
		t.Fatalf("cgb 10y = %+v (want ok/168.0)", p)
	}
	if p := getPt("cgb", "5y"); p == nil || p.Quality != "missing" {
		t.Fatalf("cgb 5y = %+v (want missing)", p)
	}
	if p := getPt("bank_aaa", "10y"); p == nil || p.Quality != "missing" {
		t.Fatalf("bank_aaa 10y = %+v (want missing)", p)
	}
	// 同日对齐：credit_spread_3y 两腿新日齐 → 41.0bp
	var credit *v1.CnfiDerivedMetric
	for _, m := range msg.Metrics {
		if m.Key == "credit_spread_3y" {
			credit = m
		}
	}
	if credit == nil || credit.Value != 41.0 {
		t.Fatalf("credit_spread_3y = %+v (want 41.0bp)", credit)
	}
	// 禁动作词（C11）：信号消息服务端面保证，响应侧复核
	for _, sig := range msg.Signals {
		for _, w := range []string{"买入", "卖出", "执行", "加仓", "减仓", "再平衡"} {
			if strings.Contains(sig.Message, w) {
				t.Fatalf("signal message contains action word %q: %s", w, sig.Message)
			}
		}
	}
}
