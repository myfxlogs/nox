// residual_recon_test.go —— D-539②：已知划转场景等式收敛（零告警）+未解释残差触 critical。
package dashboard

import (
	"context"
	"fmt"
	"testing"
	"time"

	"nox/internal/platform/testutil/storetest"
	"nox/internal/store/pgstore"
)

func d536Store(t *testing.T) *pgstore.Store {
	t.Helper()
	s := storetest.SharedStore(t, "NOX_TEST_PG_DSN")
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx,
		`TRUNCATE fin_venue_account_snapshot, fin_venue_io, income_events, alerts, fin_dci_order RESTART IDENTITY`); err != nil {
		t.Fatalf("TRUNCATE: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `DELETE FROM exchange_configs WHERE venue='d536bn'`); err != nil {
		t.Fatalf("cfg clean: %v", err)
	}
	// spot/earn 两池各一配置行（config_id 归属）。
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO exchange_configs (venue, venue_type, api_key, api_secret) VALUES
		 ('d536bn','spot','x','x'), ('d536bn','earn','x','x')`); err != nil {
		t.Fatalf("seed cfg: %v", err)
	}
	return s
}

func d536Snap(vt, ccy string, equity float64, ts time.Time) pgstore.VenueAccountSnapshot {
	return pgstore.VenueAccountSnapshot{Venue: "d536bn", VenueType: vt, Currency: ccy,
		EquityRaw: equity, FetchedAt: ts, Src: "test"}
}

func TestD539_ResidRecon(t *testing.T) {
	s := d536Store(t)
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)
	// 重放场景：spot 1000 → spot 800 + earn 200（划转台账解释；等式收敛零告警）。
	cfgSpot, _ := s.ExchangeConfigID(ctx, "d536bn", "spot")
	cfgEarn, _ := s.ExchangeConfigID(ctx, "d536bn", "earn")
	for _, sn := range []pgstore.VenueAccountSnapshot{
		d536Snap("spot", "USDT", 1000, base), d536Snap("earn", "USDT", 0, base),
	} {
		if err := s.InsertVenueAccountSnapshots(ctx, []pgstore.VenueAccountSnapshot{sn}); err != nil {
			t.Fatalf("seed snap: %v", err)
		}
	}
	if _, err := s.InsertVenueIO(ctx, []pgstore.VenueIO{{
		Venue: "d536bn", ConfigID: cfgEarn, Direction: "transfer", Ccy: "USDT",
		Amount: 200, ExternalID: "se-p1", Ts: base.Add(10 * time.Minute),
		FromWallet: "spot", ToWallet: "earn",
	}}); err != nil {
		t.Fatalf("seed transfer: %v", err)
	}
	r := NewResidualRecon(s)
	now := base.Add(time.Hour)
	for _, sn := range []pgstore.VenueAccountSnapshot{
		d536Snap("spot", "USDT", 800, now), d536Snap("earn", "USDT", 200, now),
	} {
		row := sn
		if err := r.evaluate(ctx, &row); err != nil {
			t.Fatalf("eval %s: %v", row.VenueType, err)
		}
	}
	var n int
	if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM alerts WHERE message LIKE '%残差对账%'`).Scan(&n); err != nil {
		t.Fatalf("alerts: %v", err)
	}
	if n != 0 {
		t.Fatalf("已解释划转不应触警: alerts=%d", n)
	}
	// 未解释残差：spot 再变 −50（无台账）→ critical。
	if err := r.evaluate(ctx, &pgstore.VenueAccountSnapshot{
		Venue: "d536bn", VenueType: "spot", Currency: "USDT",
		EquityRaw: 750, FetchedAt: now.Add(time.Minute), Src: "test",
	}); err != nil {
		t.Fatalf("eval2: %v", err)
	}
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM alerts WHERE level='critical' AND message LIKE '%残差对账%'`).Scan(&n); err != nil {
		t.Fatalf("alerts2: %v", err)
	}
	if n != 1 {
		t.Fatalf("未解释残差应触 critical: alerts=%d", n)
	}
	// transfer 行的 config 归属=申购池（earn）——划转净值按 ccy+钱包配 config 读：
	// 上面 spot 池侧账平的前提是同 config 域；这里同 venue 双 config 各行归位正确即收敛。
	_ = cfgSpot
}

// 订单生命周期残差解释（SG 10-16 实证 #32 炸双侧 critical）：
// EARN/DCI filled 申购源池−qty∧earn+qty 对称坍缩；settled 还本双侧解释既有；
// 处置回款=income 损益行 join 原单→得额。真 venue 名入测（钱包映射键域）。
func TestResidRecon_OrderLifecycle(t *testing.T) {
	s := d536Store(t)
	ctx := context.Background()
	r := NewResidualRecon(s)
	base := time.Now().Add(-2 * time.Hour)
	snap := func(venue, vt, ccy string, eq float64, ts time.Time) pgstore.VenueAccountSnapshot {
		return pgstore.VenueAccountSnapshot{Venue: venue, VenueType: vt, Currency: ccy,
			EquityRaw: eq, FetchedAt: ts, Src: "test"}
	}
	seed := func(snaps ...pgstore.VenueAccountSnapshot) {
		if err := s.InsertVenueAccountSnapshots(ctx, snaps); err != nil {
			t.Fatalf("seed snap: %v", err)
		}
	}
	eval := func(snaps ...pgstore.VenueAccountSnapshot) {
		for i := range snaps {
			if err := r.evaluate(ctx, &snaps[i]); err != nil {
				t.Fatalf("eval %s:%s: %v", snaps[i].Venue, snaps[i].VenueType, err)
			}
		}
	}
	alertCount := func() int {
		var n int
		if err := s.Pool().QueryRow(ctx,
			`SELECT count(*) FROM alerts WHERE message LIKE '%残差对账%'`).Scan(&n); err != nil {
			t.Fatalf("alerts: %v", err)
		}
		return n
	}
	order := func(id int64, venue, optType string, qty float64, status string, resolved, settled time.Time) {
		var res, set any
		if !resolved.IsZero() {
			res = resolved
		}
		if !settled.IsZero() {
			set = settled
		}
		if _, err := s.Pool().Exec(ctx, `
			INSERT INTO fin_dci_order (id, request_id, venue, underlying, opt_type, invest_ccy, qty,
			  status, created_at, resolved_at, settle_ccy, settle_qty, settled_at)
			VALUES ($1,$2,$3,'BTC',$4,'USDT',$5,$6,$7,$7,'USDT',$8,$9)`,
			id, fmt.Sprintf("recon|%s|%d", optType, id), venue, optType, qty, status,
			res, qty+2, set); err != nil {
			t.Fatalf("order %d: %v", id, err)
		}
	}
	t0 := base

	// ①binance EARN filled：spot 1000→650 ∧ earn 0→350（#32 同型，双侧坍缩）。
	order(901, "binance", "EARN", 350, "filled", t0.Add(5*time.Minute), time.Time{})
	seed(snap("binance", "spot", "USDT", 1000, t0), snap("binance", "earn", "USDT", 0, t0))
	eval(snap("binance", "spot", "USDT", 650, t0.Add(15*time.Minute)),
		snap("binance", "earn", "USDT", 350, t0.Add(15*time.Minute)))
	if n := alertCount(); n != 0 {
		t.Fatalf("EARN 申购双侧解释应零告警: %d", n)
	}

	// ②binance DCI filled：spot 650→550 ∧ earn 350→450。
	t1 := t0.Add(20 * time.Minute)
	order(902, "binance", "PUT", 100, "filled", t1.Add(2*time.Minute), time.Time{})
	eval(snap("binance", "spot", "USDT", 550, t1.Add(10*time.Minute)),
		snap("binance", "earn", "USDT", 450, t1.Add(10*time.Minute)))
	if n := alertCount(); n != 0 {
		t.Fatalf("DCI 申购双侧解释应零告警: %d", n)
	}

	// ③settled 还本双侧（既有断言零回归）：#902 翻 settled，
	// earn 450→350 本金离场 ∧ spot 550→652 本息到账。
	t2 := t1.Add(20 * time.Minute)
	if _, err := s.Pool().Exec(ctx,
		`UPDATE fin_dci_order SET status='settled', settled_at=$1 WHERE id=902`, t2.Add(3*time.Minute)); err != nil {
		t.Fatalf("settle 902: %v", err)
	}
	eval(snap("binance", "spot", "USDT", 652, t2.Add(10*time.Minute)),
		snap("binance", "earn", "USDT", 350, t2.Add(10*time.Minute)))
	if n := alertCount(); n != 0 {
		t.Fatalf("settled 还本双侧解释应零告警: %d", n)
	}

	// ④处置回款：#903 接货单（fill/settle 全在基线前不入任何窗）处置损益行
	// tran_id=id*10+7 → 得额=qty+pnl=350+2.6=352.6 落 spot（+351.59 同型）。
	t3 := t2.Add(20 * time.Minute)
	order(903, "binance", "PUT", 350, "settled", t0.Add(-time.Hour), t0.Add(-time.Hour))
	cfgSpot, _ := s.ExchangeConfigID(ctx, "binance", "spot")
	if cfgSpot == 0 {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO exchange_configs (venue, venue_type, api_key, api_secret) VALUES ('binance','spot','x','x')`); err != nil {
			t.Fatalf("cfg spot: %v", err)
		}
		cfgSpot, _ = s.ExchangeConfigID(ctx, "binance", "spot")
	}
	if _, err := s.InsertIncomeEvents(ctx, []pgstore.IncomeEvent{{
		Venue: "binance", VenueType: "spot", ConfigID: cfgSpot, Symbol: "BTC",
		IncomeType: "dci_exercise_disposal", AmountUSD: 2.6, TranID: 903*10 + 7,
		Ts: t3.Add(4 * time.Minute)}}); err != nil {
		t.Fatalf("disposal income: %v", err)
	}
	eval(snap("binance", "spot", "USDT", 1004.6, t3.Add(10*time.Minute)))
	if n := alertCount(); n != 0 {
		t.Fatalf("处置回款解释应零告警: %d", n)
	}

	// ⑤okx EARN：funding 1000→500 ∧ earn 0→500（savings purchase 只认 funding）。
	order(904, "okx", "EARN", 500, "filled", t3.Add(15*time.Minute), time.Time{})
	seed(snap("okx", "funding", "USDT", 1000, t3.Add(12*time.Minute)),
		snap("okx", "earn", "USDT", 0, t3.Add(12*time.Minute)))
	eval(snap("okx", "funding", "USDT", 500, t3.Add(22*time.Minute)),
		snap("okx", "earn", "USDT", 500, t3.Add(22*time.Minute)))
	if n := alertCount(); n != 0 {
		t.Fatalf("okx EARN 申购双侧解释应零告警: %d", n)
	}

	// ⑥okx DCI（sfp）：扣 unified 交易户=快照 'spot'，earn 池零腿——
	// spot 90→80 ∧ earn 不变应零告警；earn 侧若误加腿此处炸。
	t4 := t3.Add(30 * time.Minute)
	order(905, "okx", "PUT", 10, "filled", t4.Add(2*time.Minute), time.Time{})
	seed(snap("okx", "spot", "USDT", 90, t4))
	eval(snap("okx", "spot", "USDT", 80, t4.Add(10*time.Minute)),
		snap("okx", "earn", "USDT", 500, t4.Add(10*time.Minute)))
	if n := alertCount(); n != 0 {
		t.Fatalf("okx sfp 申购（spot 扣款/earn 零腿）应零告警: %d", n)
	}
}
