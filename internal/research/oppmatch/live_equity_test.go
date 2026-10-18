package oppmatch

// live_equity_test.go —— D-604 P2③ 消费点拉取锚（自 delegate_test.go 归位——
// 退回㉗伤④：锚错置修复）。覆盖：helper 五态+拍首单次化+消费径断言。
import (
	"context"
	"fmt"
	"strings"
	"testing"

	"nox/internal/platform/testutil"
	"nox/internal/store/pgstore"
)

// TestLivePoolEquity D-604 P2③ 锚：无流池拉败跳过（warn 留痕）/映射缺跳过/
// 有流池快照直返/mt 池不拉/未接线旧径——五态钉死「禁显示陈值禁静默」。
// 分币粒度修复增⑥⑦：键=venue:type:ccy——同 venue:type 异币种各取各值，
// 未持币种映射缺跳过（MULTI 户指纹禁入映射=NAV 指纹票锚）。
func TestLivePoolEquity(t *testing.T) {
	defer func() { liveEquityMapFn = nil }()
	ctx := context.Background()
	// ①无钩=快照直返（保旧语义）
	lm := fetchLiveEquityMap(ctx)
	if v, ok := lm.poolEquity("gate", "spot", "USDT", 100, "[t]"); !ok || v != 100 {
		t.Fatalf("无钩：%v %v", v, ok)
	}
	// ②mt 池不拉（快照直返——mt 快照流合法面）
	liveEquityMapFn = func(ctx context.Context) (map[string]float64, error) {
		return map[string]float64{"gate:spot:USDT": 500}, nil
	}
	lm = fetchLiveEquityMap(ctx)
	if v, ok := lm.poolEquity("3001157", "mt5", "USD", 100, "[t]"); !ok || v != 100 {
		t.Fatalf("mt 池误拉：%v %v", v, ok)
	}
	// ③无流池拉中→实拉值
	if v, ok := lm.poolEquity("gate", "spot", "USDT", 100, "[t]"); !ok || v != 500 {
		t.Fatalf("无流池实拉：%v %v", v, ok)
	}
	// ④拉败→跳过（ok=false）
	lm = liveEquityMap{err: fmt.Errorf("rpc timeout")}
	if _, ok := lm.poolEquity("gate", "spot", "USDT", 100, "[t]"); ok {
		t.Fatal("拉败应跳过（ok=false）")
	}
	// ⑤映射缺→跳过
	lm = liveEquityMap{m: map[string]float64{}}
	if _, ok := lm.poolEquity("gate", "spot", "USDT", 100, "[t]"); ok {
		t.Fatal("映射缺应跳过")
	}
	// ⑥分币键各取各值（MULTI 户形态：binance:earn 两币种池）
	lm = liveEquityMap{m: map[string]float64{"binance:earn:USDT": 1592, "binance:earn:DOGE": 52886}}
	if v, ok := lm.poolEquity("binance", "earn", "DOGE", 0, "[t]"); !ok || v != 52886 {
		t.Fatalf("分币键 DOGE：%v %v", v, ok)
	}
	// ⑦未持币种映射缺→跳过（venue:type 存在但该 ccy 键无=如实 0 非陈值）
	if _, ok := lm.poolEquity("binance", "earn", "BTC", 999, "[t]"); ok {
		t.Fatal("未持币种应跳过（非快照陈值回退）")
	}
}

// TestLiveEquityMap_OncePerPass 退回㉗伤③锚：拍首单次调用——
// fetchLiveEquityMap 一次后 poolEquity 复用 map（调用计数钉死，禁逐池 Refresh）。
func TestLiveEquityMap_OncePerPass(t *testing.T) {
	defer func() { liveEquityMapFn = nil }()
	ctx := context.Background()
	calls := 0
	liveEquityMapFn = func(ctx context.Context) (map[string]float64, error) {
		calls++
		return map[string]float64{"gate:spot:USDT": 500, "okx:funding:USDT": 300}, nil
	}
	lm := fetchLiveEquityMap(ctx)
	for _, tc := range [][4]string{{"gate", "spot", "USDT", "1"}, {"okx", "funding", "USDT", "2"}, {"gate", "spot", "USDT", "3"}} {
		if _, ok := lm.poolEquity(tc[0], tc[1], tc[2], 0, "[t]"); !ok {
			t.Fatalf("poolEquity %s:%s:%s 应命中", tc[0], tc[1], tc[2])
		}
	}
	if calls != 1 {
		t.Fatalf("拍首应单次调用，got %d", calls)
	}
}

// TestXferScan_LivePulled 消费径端到端锚（退回㉗伤④）：mock map→
// xferScan 提案金额=实拉值（非快照 p.Equity）。
func TestXferScan_LivePulled(t *testing.T) {
	defer func() { liveEquityMapFn = nil }()
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
		DELETE FROM fin_venue_account_snapshot WHERE venue='lvgate';
		DELETE FROM fin_cex_earn_apr;
		DELETE FROM fin_condition_gap WHERE subject LIKE 'USDT:lvgate%'`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `
		INSERT INTO fin_venue_account_snapshot
			(venue, venue_type, balance_raw, equity_raw, free_margin_raw, currency, fetched_at)
			VALUES ('lvgate','spot',50,50,50,'USDT',now());
		INSERT INTO fin_cex_earn_apr (venue, ccy, product_type, apr) VALUES
			('lvgate','USDT','flexible',0.02),('lvdst','USDT','flexible',0.09)`); err != nil {
		t.Fatal(err)
	}
	// 快照 equity=50（尘额下）；实拉 map=750——提案金额须取实拉值。
	liveEquityMapFn = func(ctx context.Context) (map[string]float64, error) {
		return map[string]float64{"lvgate:spot:USDT": 750}, nil
	}
	xferScan(ctx, st)
	var txt string
	if err := st.Pool().QueryRow(ctx,
		`SELECT request_text FROM fin_condition_gap
		 WHERE template_id='cross_venue_xfer' AND subject='USDT:lvgate>lvdst'`).Scan(&txt); err != nil {
		t.Fatalf("消费径提案缺: %v", err)
	}
	if !strings.Contains(txt, "750.00") {
		t.Fatalf("提案金额应取实拉值 750: %q", txt)
	}
	// 映射缺→该池不提案（禁显示陈值）。
	if _, err := st.Pool().Exec(ctx,
		`DELETE FROM fin_condition_gap WHERE subject LIKE 'USDT:lvgate%'`); err != nil {
		t.Fatal(err)
	}
	liveEquityMapFn = func(ctx context.Context) (map[string]float64, error) {
		return map[string]float64{}, nil
	}
	xferScan(ctx, st)
	var n int
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM fin_condition_gap WHERE subject LIKE 'USDT:lvgate%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("映射缺应禁提案（禁显示陈值）")
	}
	// 尾部自清：dst 残行会抢后续测试 argmax（共享库）。
	st.Pool().Exec(ctx, `DELETE FROM fin_cex_earn_apr WHERE venue IN ('lvgate','lvdst')`)
}
