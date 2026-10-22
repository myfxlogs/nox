// term_basis_filter_test.go —— #1938 修三断锚：venue 过滤/流动性地板/近到期
// 剔除（vol24h=0 陈价合约伪基差+okx 挂 binance 名+8 天到期=三病灶实证面）。
package oppmatch

import (
	"context"
	"testing"
	"time"

	"nox/internal/platform/testutil"
	"nox/internal/store/pgstore"
)

func TestTermBasisBest_Filters(t *testing.T) {
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
	far := time.Now().Add(60 * 24 * time.Hour) // 60d 到期（过 tenor 阈）
	near := time.Now().Add(8 * 24 * time.Hour) // 8d 到期（近到期剔——#1938 实证形）
	v := func(x float64) *float64 { return &x }
	// 插行三型（universe 写径——UpsertCexUniverse 若存在则用，直 SQL 兜底）。
	rows := []struct {
		venue, sym string
		basis, vol *float64
		exp        *time.Time
	}{
		{"binance", "XAU-USD_UM-261030", v(43.5), v(0), &far},    // 伪基差：vol24h=0 剔
		{"binance", "BTCUSDT_270326", v(5.1), v(99999), &far},    // 合格行（过阈）
		{"binance", "ETHUSDT_NEAREXP", v(20.0), v(99999), &near}, // 近到期剔
		{"okx", "OKX_FUT_1", v(30.0), v(99999), &far},            // 异所剔（不挂 binance 名）
	}
	for _, r := range rows {
		if _, err := st.Pool().Exec(ctx, `INSERT INTO fin_cex_universe
			(venue, symbol, base, status, contract_type, last_basis_ann, last_vol24h, expiry_ts)
			VALUES ($1,$2,'BTC','TRADING','FUTURES',$3,$4,$5)
			ON CONFLICT (venue, symbol) DO UPDATE SET last_basis_ann=$3, last_vol24h=$4, expiry_ts=$5`,
			r.venue, r.sym, r.basis, r.vol, r.exp); err != nil {
			t.Fatalf("universe 插行 %s: %v", r.sym, err)
		}
	}
	best, err := termBasisBest(ctx, st, "binance")
	if err != nil {
		t.Fatal(err)
	}
	if best == nil {
		t.Fatal("合格行在册应回最优")
	}
	if best.Symbol != "BTCUSDT_270326" {
		t.Fatalf("最优行应=BTCUSDT_270326（vol0/近到期/异所全剔），得 %s", best.Symbol)
	}
	if *best.BasisAnn != 5.1 {
		t.Fatalf("BasisAnn=%v want 5.1", *best.BasisAnn)
	}
	// 异所面——okx 域查询应得自身行（不串 binance）。
	b2, err := termBasisBest(ctx, st, "okx")
	if err != nil || b2 == nil || b2.Symbol != "OKX_FUT_1" {
		t.Fatalf("okx 域最优行错: %v %v", b2, err)
	}
}
