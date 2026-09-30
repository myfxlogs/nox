// service_capital_coverage_test.go —— D-359 S4：资金不眠覆盖率 RPC 锚。
// 数值锚与 digest D-356/D-357（同 SSOT 双实现）同源：TRX 余额=balance+tron_power 口径锚
// （D-356-R1 教训）+汇总两数（覆盖率/加权年化）+空态（rows 空=「数据源全缺」语义）。
package dashboard

import (
	"context"
	"strings"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/store/pgstore"

	v1 "arb/internal/gen/dashboardv1"
)

// covTestStore 覆盖率测试夹具库：TRUNCATE 本包触面全部源表（其余测试同库隔离）。
func covTestStore(t *testing.T) *pgstore.Store {
	t.Helper()
	s := serviceTestStoreSingleInit(t)
	ctx := context.Background()
	for _, tbl := range []string{
		"fin_trx_account", "fin_trx_txs", "fin_trx_vote_apr", "position_daily_income",
		"fin_cmc_prices", "fin_cmc_account_summary", "fin_cmc_positions", "positions",
		"fin_venue_account_snapshot", "capital_snapshots",
	} {
		if _, err := s.Pool().Exec(ctx, "TRUNCATE TABLE "+tbl+" RESTART IDENTITY CASCADE"); err != nil {
			t.Fatalf("TRUNCATE %s: %v", tbl, err)
		}
	}
	return s
}

func covTestService(s *pgstore.Store) *Service {
	return &Service{store: s, qbus: bus.New(), freshTTL: time.Minute, liqMMR: 0.005}
}

// covResponse 便捷取 response。
func covResponse(ctx context.Context, t *testing.T, svc *Service) *v1.GetCapitalCoverageResponse {
	t.Helper()
	resp, err := svc.GetCapitalCoverage(ctx, nil)
	if err != nil {
		t.Fatalf("GetCapitalCoverage: %v", err)
	}
	return resp.Msg
}

// TestGetCapitalCoverage_TrxBalanceAnchor D-356-R1 口径锚：TRX 余额=(balance+tron_power)/1e6×价。
// balance 20000 + tron_power 10000（sun×1e6）= 30000 TRX × mid 0.5 = $15000；vote 4.0% 单年化。
func TestGetCapitalCoverage_TrxBalanceAnchor(t *testing.T) {
	s := covTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := s.InsertTRXAccount(ctx, pgstore.TRXAccount{
		Address:    "TWsbXR6SPvvXPxUtPMQvEj7AMH55555555",
		BalanceSun: 20000000000, TronPower: 10000000000, EnergyDelegated: 10000000000,
		FetchedAt: now,
	}); err != nil {
		t.Fatalf("InsertTRXAccount: %v", err)
	}
	if err := s.InsertTRXVoteApr(ctx, pgstore.TRXVoteApr{FetchedAt: now, CurrentRate: 4.0}); err != nil {
		t.Fatalf("InsertTRXVoteApr: %v", err)
	}
	if err := s.InsertCMCPrice(ctx, pgstore.CMCPrice{
		InstrumentID: "X-AFQUL", MidPrice: 0.5, MarketStatus: "Open",
		QuoteTime: now, FetchedAt: now,
	}); err != nil {
		t.Fatalf("InsertCMCPrice: %v", err)
	}
	resp := covResponse(ctx, t, covTestService(s))
	if len(resp.Rows) == 0 || resp.Rows[0].Pool != "TRX 质押+投票+能量" {
		t.Fatalf("rows[0] 应为 TRX 行, got %+v", resp.Rows)
	}
	trx := resp.Rows[0]
	if !trx.HasBalance || trx.BalanceUsd != 15000 {
		t.Errorf("TRX balance_usd = %f (has=%v), want 15000 = (20000+10000)/1e6×0.5", trx.BalanceUsd, trx.HasBalance)
	}
	if !trx.HasApr || trx.AprPct != 4.0 {
		t.Errorf("TRX apr = %f (has=%v), want 4.0（能量冷启动 0 样本不计）", trx.AprPct, trx.HasApr)
	}
	// 汇总：单行真金在产 → total=productive=15000，覆盖率 100%
	if !resp.HasSummary || resp.TotalUsd != 15000 || resp.ProductiveUsd != 15000 || resp.CoveragePct != 100 {
		t.Errorf("summary = total %f productive %f coverage %f (has=%v), want 15000/15000/100",
			resp.TotalUsd, resp.ProductiveUsd, resp.CoveragePct, resp.HasSummary)
	}
}

// TestGetCapitalCoverage_SummaryWeights 汇总两数锚（与 digest T1 同源数值）：
// TRX 30000@4.0 在产 + CMC 10000 持仓中在产 + live 10000@5.0 在产 + paper 50000 非真金 +
// venue USDT 8000 闲置（入分母不入分子）→ 总 $58000 在产 $50000 覆盖率 86.2%；
// 加权=(30000×4+10000×5)/40000=4.25（CMC/venue 无年化不进）。
func TestGetCapitalCoverage_SummaryWeights(t *testing.T) {
	s := covTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := s.InsertTRXAccount(ctx, pgstore.TRXAccount{
		Address:    "TWsbXR6SPvvXPxUtPMQvEj7AMH55555555",
		BalanceSun: 30000000000, EnergyDelegated: 30000000000, FetchedAt: now,
	}); err != nil {
		t.Fatalf("InsertTRXAccount: %v", err)
	}
	if err := s.InsertTRXVoteApr(ctx, pgstore.TRXVoteApr{FetchedAt: now, CurrentRate: 4.0}); err != nil {
		t.Fatalf("InsertTRXVoteApr: %v", err)
	}
	if err := s.InsertCMCPrice(ctx, pgstore.CMCPrice{
		InstrumentID: "X-AFQUL", MidPrice: 1.0, MarketStatus: "Open", QuoteTime: now, FetchedAt: now,
	}); err != nil {
		t.Fatalf("InsertCMCPrice: %v", err)
	}
	if err := s.InsertCMCAccountSummary(ctx, pgstore.CMCAccountSummary{
		AccountID: 1, TotalAccountValue: 10000, FetchedAt: now,
	}); err != nil {
		t.Fatalf("InsertCMCAccountSummary: %v", err)
	}
	if err := s.InsertCMCPositions(ctx, 1, []pgstore.CMCPosition{{
		InstrumentID: "X-AFQUL", Direction: "Sell", UnrealisedPnL: -123.45,
	}}); err != nil {
		t.Fatalf("InsertCMCPositions: %v", err)
	}
	// live 10000@carry 折算 5.0%：日均 carry=10000×5/36500≈1.3699
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO positions (opportunity_id, type, status, environment, notional_usd)
		VALUES ('opp1','XASSET_CARRY','OPEN','live',10000)`); err != nil {
		t.Fatalf("seed live position: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO positions (opportunity_id, type, status, environment, notional_usd)
		VALUES ('opp2','XASSET_CARRY','OPEN','demo',50000)`); err != nil {
		t.Fatalf("seed paper position: %v", err)
	}
	day := now.Truncate(24 * time.Hour)
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO position_daily_income (day, energy_trx, voting_trx, interest_usd, price)
		VALUES ($1, 0.6849315068493151, 0, 0.6849315068493151, 1.0)`, day); err != nil {
		t.Fatalf("seed daily income: %v", err)
	}
	svc := covTestService(s)
	eq8k := 8000.0 // #1c：账户面=acctMon 实时态
	seedLiveAccounts(t, svc, []pgstore.VenueAccountLatest{
		{Venue: "binance", VenueType: "perp", EquityRaw: &eq8k, Currency: "USDT", FetchedAt: now},
	})
	resp := covResponse(ctx, t, svc)
	// D-360 S4：paper 行不再出现（demo/纸面=噪声不入面板）——行序 TRX→CMC→live→venue
	if len(resp.Rows) != 4 {
		t.Fatalf("rows len = %d, want 4:", len(resp.Rows))
	}
	for i, want := range []string{"TRX 质押+投票+能量", "CMC 账户（手动持仓监控域）", "系统持仓（live）", "venue账户 binance/perp"} {
		if resp.Rows[i].Pool != want {
			t.Errorf("rows[%d] = %q, want %q", i, resp.Rows[i].Pool, want)
		}
	}
	for _, r := range resp.Rows {
		if r.Pool == "系统持仓（paper）" {
			t.Error("paper 行应不出现（D-360 S4：demo/纸面=噪声不入）")
		}
	}
	// 状态与权重语义
	if got := resp.Rows[1].Status; got != "手动持仓中（浮盈 $-123.45）" {
		t.Errorf("CMC status = %q, want 手动持仓中（浮盈 $-123.45）", got)
	}
	if !resp.Rows[3].RealMoney || resp.Rows[3].Productive {
		t.Error("venue USDT 行应 RealMoney=true∧Productive=false（入分母的睡钱）")
	}
	// 汇总两数：总 30000+10000+10000+8000=58000；在产=TRX30000+CMC10000+live10000=50000
	if !resp.HasSummary || resp.TotalUsd != 58000 || resp.ProductiveUsd != 50000 {
		t.Errorf("total=%f productive=%f (has=%v), want 58000/50000",
			resp.TotalUsd, resp.ProductiveUsd, resp.HasSummary)
	}
	if cov := resp.ProductiveUsd / resp.TotalUsd * 100; cov < 86.1 || cov > 86.3 {
		t.Errorf("coverage = %f, want ~86.2", cov)
	}
	// 加权年化=(30000×4+10000×5)/40000=4.25；venue/CMC/paper 无年化不进
	if !resp.HasWeighted || resp.WeightedApr < 4.2499 || resp.WeightedApr > 4.2501 {
		t.Errorf("weighted_apr = %f (has=%v), want 4.25", resp.WeightedApr, resp.HasWeighted)
	}
}

// TestGetCapitalCoverage_Empty 全源空 → 仅 live「零持仓」行（D-356 A8 明文预期行，
// 非真金不入汇总）+has_summary/has_weighted=false+口径注记随行。
func TestGetCapitalCoverage_Empty(t *testing.T) {
	s := covTestStore(t)
	ctx := context.Background()
	resp := covResponse(ctx, t, covTestService(s))
	if len(resp.Rows) != 1 || resp.Rows[0].Pool != "系统持仓（live）" || resp.Rows[0].Status != "零持仓" {
		t.Fatalf("全源空应仅零持仓行, got %d 行: %+v", len(resp.Rows), resp.Rows)
	}
	if resp.Rows[0].HasBalance || resp.Rows[0].RealMoney {
		t.Errorf("零持仓行应无余额非真金, got has_balance=%v real_money=%v", resp.Rows[0].HasBalance, resp.Rows[0].RealMoney)
	}
	if resp.HasSummary || resp.HasWeighted {
		t.Errorf("空态 has_summary/has_weighted 应 false, got %v/%v", resp.HasSummary, resp.HasWeighted)
	}
	if resp.CaliberNote == "" {
		t.Error("口径注记应随响应返回")
	}
}

// TestGetCapitalCoverage_VenueUSDDenominator D-361 S1 锚：USDT/USD 同视 1:1 入总资金分母
// （业主令），UST 仍排除（「—」+note 不猜）。
func TestGetCapitalCoverage_VenueUSDDenominator(t *testing.T) {
	s := covTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	svc := covTestService(s)
	eqA, eqB, eqC := 100.0, 500.0, 700.0 // #1c：账户面=acctMon 实时态
	seedLiveAccounts(t, svc, []pgstore.VenueAccountLatest{
		{Venue: "binance", VenueType: "spot", EquityRaw: &eqA, Currency: "USDT", FetchedAt: now},
		{Venue: "1279967", VenueType: "mt5", EquityRaw: &eqB, Currency: "USD", FetchedAt: now},
		{Venue: "15032313", VenueType: "mt5", EquityRaw: &eqC, Currency: "UST", FetchedAt: now},
	})
	resp := covResponse(ctx, t, svc)
	var usdRow, ustRow *v1.CapitalCoverageRow
	for _, r := range resp.Rows {
		if r.Pool == "venue账户 1279967/mt5" {
			usdRow = r
		}
		if r.Pool == "venue账户 15032313/mt5" {
			ustRow = r
		}
	}
	if usdRow == nil || !usdRow.HasBalance || usdRow.BalanceUsd != 500 {
		t.Fatalf("USD 行应入分母（HasBalance=500）, got %+v", usdRow)
	}
	if ustRow == nil || ustRow.HasBalance {
		t.Fatalf("UST 行应无 USD 价（HasBalance=false）, got %+v", ustRow)
	}
	// 总资金含 USD 行：total ≥ 500（=100 USDT+500 USD），UST 700 不入
	if !resp.HasSummary || resp.TotalUsd != 600 {
		t.Errorf("total = %f (has=%v), want 600（USDT 100+USD 500；UST 700 排除）", resp.TotalUsd, resp.HasSummary)
	}
	if !strings.Contains(resp.CaliberNote, "CEX=USDT 标余额，USDT/LDUSDT 计、杂币不折算；MT=USD 同视") {
		t.Errorf("口径注记应含 USDT/USD 同文: %s", resp.CaliberNote)
	}
}
