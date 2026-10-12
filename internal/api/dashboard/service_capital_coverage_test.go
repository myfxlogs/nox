// service_capital_coverage_test.go —— D-359 S4：资金不眠覆盖率 RPC 锚。
// 数值锚与 digest D-356/D-357（同 SSOT 双实现）同源：TRX 余额=balance+tron_power 口径锚
// （D-356-R1 教训）+汇总两数（覆盖率/加权年化）+空态（rows 空=「数据源全缺」语义）。
package dashboard

import (
	"context"
	"strings"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/store/pgstore"

	v1 "nox/internal/gen/dashboardv1"
)

// covTestStore 覆盖率测试夹具库：TRUNCATE 本包触面全部源表（其余测试同库隔离）。
func covTestStore(t *testing.T) *pgstore.Store {
	t.Helper()
	s := serviceTestStoreSingleInit(t)
	ctx := context.Background()
	for _, tbl := range []string{
		"fin_trx_account", "fin_trx_txs", "fin_trx_vote_apr", "position_daily_income",
		"positions", "fin_venue_account_snapshot", "ticks",
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
	if err := s.EnsureMonthPartition(ctx, now.Year(), int(now.Month())); err != nil {
		t.Fatalf("EnsureMonthPartition: %v", err)
	}
	if err := s.InsertTicks(ctx, []pgstore.TickRow{
		{Ts: now, Venue: "binance", Symbol: "TRXUSDT", Bid: 0.5, Ask: 0.5, VenueType: "spot"},
	}); err != nil {
		t.Fatalf("InsertTicks: %v", err)
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

// TestGetCapitalCoverage_SummaryWeights 汇总两数锚（D-127 新口径，与 digest T1 同源数值）：
// TRX 30000@4.0 在产 + venue binance·永续 8000（fm=7500→占用 500）
// + live 仓名义 10000（降 note 参考，不入合计）+ demo 50000 不入面板（CMC 行随族件剔除）。
// 总资金=Σ净值+TRX=38000（名义额退出分母）；在产=30000+占用500=30500 → 覆盖率 80.3%；
// 加权=(30000×4+占用500×5)/(30000+500)=4.0164（占用行按占用额加权，venue 无年化不进）。
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
	if err := s.EnsureMonthPartition(ctx, now.Year(), int(now.Month())); err != nil {
		t.Fatalf("EnsureMonthPartition: %v", err)
	}
	if err := s.InsertTicks(ctx, []pgstore.TickRow{
		{Ts: now, Venue: "binance", Symbol: "TRXUSDT", Bid: 1.0, Ask: 1.0, VenueType: "spot"},
	}); err != nil {
		t.Fatalf("InsertTicks: %v", err)
	}
	// live 仓名义 10000@carry 折算 5.0%：日均 carry=10000×5/36500≈1.3699（名义额仅入 APR 折算+note 参考）
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
	eq8k, fm7500 := 8000.0, 7500.0 // #1c：账户面=acctMon 实时态（fm 已采→占用=eq−fm=500）
	seedLiveAccounts(t, svc, []pgstore.VenueAccountLatest{
		{Venue: "binance", VenueType: "perp", EquityRaw: &eq8k, FreeMarginRaw: &fm7500, Currency: "USDT", FetchedAt: now},
	})
	resp := covResponse(ctx, t, svc)
	// D-360 S4：paper 行不再出现（demo/纸面=噪声不入面板）——D-127 行序 TRX→占用→venue
	if len(resp.Rows) != 3 {
		t.Fatalf("rows len = %d, want 3: %+v", len(resp.Rows), resp.Rows)
	}
	for i, want := range []string{"TRX 质押+投票+能量", "保证金占用", "binance·合约"} {
		if resp.Rows[i].Pool != want {
			t.Errorf("rows[%d] = %q, want %q", i, resp.Rows[i].Pool, want)
		}
	}
	for _, r := range resp.Rows {
		if r.Pool == "系统持仓（纸面）" {
			t.Error("paper 行应不出现（D-360 S4：demo/纸面=噪声不入）")
		}
	}
	// D-127 占用行：值=eq−fm=500；RealMoney=false（净值子集视图不入分母）∧Productive=true（入分子）
	occRow := resp.Rows[1]
	if !occRow.HasBalance || occRow.BalanceUsd != 500 || occRow.RealMoney || !occRow.Productive {
		t.Errorf("占用行应 HasBalance=500 RealMoney=false Productive=true, got has=%v bal=%f real=%v prod=%v",
			occRow.HasBalance, occRow.BalanceUsd, occRow.RealMoney, occRow.Productive)
	}
	if !strings.Contains(occRow.Note, "账户级占用") || !strings.Contains(occRow.Note, "名义额参考 $10000.00") {
		t.Errorf("占用行 note 应含账户级边界+名义额参考: %q", occRow.Note)
	}
	if !occRow.HasApr || occRow.AprPct < 4.9999 || occRow.AprPct > 5.0001 {
		t.Errorf("占用行年化应 5.0（按名义额折算）, got %f has=%v", occRow.AprPct, occRow.HasApr)
	}
	// D-129：占用明细收说明列
	if occRow.Status != "占用中" || !strings.Contains(occRow.Note, "；占用明细 binance·合约 $500.00") {
		t.Errorf("占用行 status/note = %q/%q, want 占用中+占用明细 binance·合约 $500.00", occRow.Status, occRow.Note)
	}
	// venue 行：入分母不入分子，note 带「可用」；状态=业务词（occ>0→部分占用）
	venRow := resp.Rows[2]
	if !venRow.RealMoney || venRow.Productive {
		t.Error("venue USDT 行应 RealMoney=true∧Productive=false（入分母的睡钱）")
	}
	if !strings.Contains(venRow.Note, "可用 7500.00") {
		t.Errorf("venue 行 note 应含可用额: %q", venRow.Note)
	}
	if venRow.Status != "部分占用" {
		t.Errorf("venue 行 status = %q, want 部分占用（occ=500>0）", venRow.Status)
	}
	// 汇总两数：总=Σ净值+TRX=38000（占用行 RealMoney=false 不入分母）；在产=30000+占用500=30500
	if !resp.HasSummary || resp.TotalUsd != 38000 || resp.ProductiveUsd != 30500 {
		t.Errorf("total=%f productive=%f (has=%v), want 38000/30500",
			resp.TotalUsd, resp.ProductiveUsd, resp.HasSummary)
	}
	if cov := resp.ProductiveUsd / resp.TotalUsd * 100; cov < 80.2 || cov > 80.4 {
		t.Errorf("coverage = %f, want ~80.3", cov)
	}
	// 加权年化=(30000×4+占用500×5)/30500=4.0164（占用行按占用额加权；venue 无年化不进）
	if !resp.HasWeighted || resp.WeightedApr < 4.0163 || resp.WeightedApr > 4.0165 {
		t.Errorf("weighted_apr = %f (has=%v), want ~4.0164", resp.WeightedApr, resp.HasWeighted)
	}
}

// TestGetCapitalCoverage_Empty 全源空 → 仅「保证金占用·零占用」行（D-356 A8 预期行的
// D-127 形态：HasBalance=false 非真金不入汇总）+has_summary/has_weighted=false+口径注记随行。
func TestGetCapitalCoverage_Empty(t *testing.T) {
	s := covTestStore(t)
	ctx := context.Background()
	resp := covResponse(ctx, t, covTestService(s))
	if len(resp.Rows) != 1 || resp.Rows[0].Pool != "保证金占用" || resp.Rows[0].Status != "零占用" {
		t.Fatalf("全源空应仅零占位行, got %d 行: %+v", len(resp.Rows), resp.Rows)
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
		if r.Pool == "1279967·MT5" {
			usdRow = r
		}
		if r.Pool == "15032313·MT5" {
			ustRow = r
		}
	}
	if usdRow == nil || !usdRow.HasBalance || usdRow.BalanceUsd != 500 {
		t.Fatalf("USD 行应入分母（HasBalance=500）, got %+v", usdRow)
	}
	// D-130 域3：UST 行原币直显（HasBalance=true 供展示）但不入合计（币种门拒纳）
	if ustRow == nil || !ustRow.HasBalance || ustRow.BalanceCcy != "UST" || ustRow.Status != "未折算" {
		t.Fatalf("UST 行应原币直显未折算, got %+v", ustRow)
	}
	// 总资金含 USD 行：total ≥ 500（=100 USDT+500 USD），UST 700 不入
	if !resp.HasSummary || resp.TotalUsd != 600 {
		t.Errorf("total = %f (has=%v), want 600（USDT 100+USD 500；UST 700 排除）", resp.TotalUsd, resp.HasSummary)
	}
	if !strings.Contains(resp.CaliberNote, "CEX=USDT 标余额，USDT/LDUSDT 计、杂币不折算；MT=USD 同视") {
		t.Errorf("口径注记应含 USDT/USD 同文: %s", resp.CaliberNote)
	}
}

// TestGetCapitalCoverage_MTFrameCurrencyFallback D-126 锚：MT 流帧币种恒空（MT5 帧无
// Currency 字段→Funds.Currency 如实空串）——live 命中不得覆盖快照行币种：
// ①快照 USD → MT 行落余额进 TotalUsd 分母，note 不误标「非 USDT/USD 不折算」；
// ②快照币种亦空 → 余额列仍「—」（fail-closed 不破，不猜）。
func TestGetCapitalCoverage_MTFrameCurrencyFallback(t *testing.T) {
	s := covTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	svc := covTestService(s)
	eqUSD, eqNoCcy := 4156.52, 700.0
	seedLiveAccounts(t, svc, []pgstore.VenueAccountLatest{
		{Venue: "3001157", VenueType: "mt5", EquityRaw: &eqUSD, Currency: "USD", FetchedAt: now},
		{Venue: "15032313", VenueType: "mt5", EquityRaw: &eqNoCcy, Currency: "", FetchedAt: now},
	})
	// 流帧直存帧缓存（绕 ObserveStreamAccount 的 Kick 副作用）；帧币种恒空=生产实况形态。
	svc.acctMon.streamAccts.Store("3001157:mt5", StreamAcct{Equity: eqUSD, FreeMargin: eqUSD, Currency: "", FrameTime: now})
	svc.acctMon.streamAccts.Store("15032313:mt5", StreamAcct{Equity: eqNoCcy, FreeMargin: eqNoCcy, Currency: "", FrameTime: now})
	resp := covResponse(ctx, t, svc)
	var usdRow, noCcyRow *v1.CapitalCoverageRow
	for _, r := range resp.Rows {
		if r.Pool == "3001157·MT5" {
			usdRow = r
		}
		if r.Pool == "15032313·MT5" {
			noCcyRow = r
		}
	}
	if usdRow == nil {
		t.Fatalf("3001157·MT5 行应出现: %+v", resp.Rows)
	}
	// ① live 帧净值 + 快照 USD 币种 → 入分母
	if !usdRow.HasBalance || usdRow.BalanceUsd != 4156.52 || !usdRow.RealMoney {
		t.Errorf("MT USD 行应 HasBalance=4156.52, got has=%v bal=%f real=%v",
			usdRow.HasBalance, usdRow.BalanceUsd, usdRow.RealMoney)
	}
	if strings.Contains(usdRow.Note, "非 USDT/USD 不折算") {
		t.Errorf("USD 行 note 不应误标不折算: %q", usdRow.Note)
	}
	if !strings.Contains(usdRow.Note, "活账本") {
		t.Errorf("USD 行 note 应为活账本命中注记: %q", usdRow.Note)
	}
	// ② 快照币种亦空 → 「—」（HasBalance=false 不入分母）
	if noCcyRow == nil || noCcyRow.HasBalance {
		t.Errorf("快照币种空行应 HasBalance=false（诚实「—」）, got %+v", noCcyRow)
	}
	// 汇总分母：仅 USD MT 行（零持仓行非真金不入）
	if !resp.HasSummary || resp.TotalUsd != 4156.52 {
		t.Errorf("TotalUsd = %f (has=%v), want 4156.52（MT 真金入分母）", resp.TotalUsd, resp.HasSummary)
	}
}
