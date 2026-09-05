// D-212 S2 测试：GetHedgeStructure（镜像恒和=0/carry 手算/单位断言/net_exposure 真实计算）+ ListHedgeFlow RPC。
package dashboard

import (
	"context"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"arb/internal/collect/cmc"
	"arb/internal/store/pgstore"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"

	"connectrpc.com/connect"
)

// hedgeTestStore 创建测试 store（ARB_TEST_PG_SERVICE_DSN，TRUNCATE 全库含 fin_cmc_*）。
func hedgeTestStore(t *testing.T) *pgstore.Store {
	t.Helper()
	s := trxTestStore(t) // 复用 trxTestStore（TRUNCATE fin_trx_*/position_daily_income/personal_positions）
	ctx := context.Background()
	// 额外清 fin_cmc_* 表（trxTestStore 未含，D-212 跨源测试需要）
	for _, tbl := range []string{"fin_cmc_history", "fin_cmc_positions", "fin_cmc_account_summary", "fin_cmc_prices"} {
		if _, err := s.Pool().Exec(ctx, "TRUNCATE TABLE "+tbl+" RESTART IDENTITY CASCADE"); err != nil {
			t.Fatalf("TRUNCATE %s: %v", tbl, err)
		}
	}
	return s
}

// TestGetHedgeStructure_PlatformCloseoutSource 验证强平价标注切换为平台 50% 规则。
func TestGetHedgeStructure_PlatformCloseoutSource(t *testing.T) {
	s := hedgeTestStore(t)
	ctx := context.Background()
	if err := s.InsertCMCAccountSummary(ctx, pgstore.CMCAccountSummary{
		AccountID:               1,
		TotalEquityUsedInMargin: 600.83,
		FetchedAt:               time.Now().UTC(),
	}); err != nil {
		t.Fatalf("InsertCMCAccountSummary: %v", err)
	}

	svc := &Service{store: s, freshTTL: time.Minute, liqMMR: 0.005}
	resp, err := svc.GetHedgeStructure(ctx, connect.NewRequest(&v1.GetHedgeStructureRequest{}))
	if err != nil {
		t.Fatalf("GetHedgeStructure: %v", err)
	}
	if got := resp.Msg.Structure.CloseoutSource; got != "platform_rule_50" {
		t.Fatalf("closeout_source = %q, want %q", got, "platform_rule_50")
	}
}

// TestGetHedgeStructure_PlatformLevels 验证平台强平/预警净值均以 used margin 为基数计算。
func TestGetHedgeStructure_PlatformLevels(t *testing.T) {
	s := hedgeTestStore(t)
	ctx := context.Background()
	if err := s.InsertCMCAccountSummary(ctx, pgstore.CMCAccountSummary{
		AccountID:               1,
		TotalEquityUsedInMargin: 600.83,
		FetchedAt:               time.Now().UTC(),
	}); err != nil {
		t.Fatalf("InsertCMCAccountSummary: %v", err)
	}

	svc := &Service{store: s, freshTTL: time.Minute, liqMMR: 0.005}
	resp, err := svc.GetHedgeStructure(ctx, connect.NewRequest(&v1.GetHedgeStructureRequest{}))
	if err != nil {
		t.Fatalf("GetHedgeStructure: %v", err)
	}
	st := resp.Msg.Structure
	if diff := st.CloseoutLevelUsd - 600.83*0.5; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("closeout_level_usd = %v, want %v", st.CloseoutLevelUsd, 600.83*0.5)
	}
	if diff := st.WarningLevelUsd - 600.83*0.8; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("warning_level_usd = %v, want %v", st.WarningLevelUsd, 600.83*0.8)
	}
}

// TestHedgeStructurePlatformLabel 验证前端只渲染平台口径标注及其标定提示。
func TestHedgeStructurePlatformLabel(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(testFile), "..", "..", "web", "src", "components", "PositionsPageHedgeStructure.tsx")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read hedge structure component: %v", err)
	}
	source := string(data)
	if !strings.Contains(source, `structure!.closeoutSource === "platform_rule_50"`) {
		t.Fatal("frontend does not gate the label on platform_rule_50")
	}
	if !strings.Contains(source, "平台口径 · 50% 规则") {
		t.Fatal("frontend is missing the platform-rule label")
	}
	if !strings.Contains(source, "CMC Balance 面板标定：强平=保证金 50%，预警=保证金 80%；规则变更后需复核") {
		t.Fatal("frontend is missing the platform calibration tooltip")
	}
	if strings.Contains(source, "参考估算（自算）") {
		t.Fatal("retired self-estimate label remains in frontend")
	}
}

// TestGetHedgeStructure_MirrorPnlSumZero D-236 变更9 改写：镜像恒和=0 退役（业主真值原则取代）。
// 新断言：空单 PnL=平台自报值（freshLive 回退 DB unrealised_pnl，无 cmcLiveProvider 时）。
// 构造 TRX 账户（18070 质押）+ CMC 持仓（18100 空单，entry=0.33171，unrealised_pnl=-117.83）+ CMC price（mid=0.3236）。
func TestGetHedgeStructure_MirrorPnlSumZero(t *testing.T) {
	s := hedgeTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// TRX 账户快照（18070 票权，97.86 TRX 余额）
	if err := s.InsertTRXAccount(ctx, pgstore.TRXAccount{
		Address: "TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", VotesTotal: 18070,
		BalanceSun: 97863827, AllowanceSun: 7717994,
		EnergyDelegated: 17270000000, BandwidthStaked: 800000000,
		FetchedAt: now,
	}); err != nil {
		t.Fatalf("InsertTRXAccount: %v", err)
	}
	// CMC 持仓（空单 18100，entry=0.33171，unrealised_pnl=-117.83）
	if err := s.InsertCMCPositions(ctx, 1, []pgstore.CMCPosition{{
		AccountID: 1, InstrumentID: "X-AFQUL", Direction: "SHORT", Quantity: 18100,
		EntryPrice: 0.33171, UnrealisedPnL: -117.83, MarginPercentage: 0.1,
		EvaluationAskPrice: 0.32335, EvaluationBidPrice: 0.32285,
		FetchedAt: now,
	}}); err != nil {
		t.Fatalf("InsertCMCPositions: %v", err)
	}
	// CMC price（mid=0.3236）
	if err := s.InsertCMCPrice(ctx, pgstore.CMCPrice{
		InstrumentID: "X-AFQUL", MidPrice: 0.3236, FetchedAt: now,
	}); err != nil {
		t.Fatalf("InsertCMCPrice: %v", err)
	}
	// CMC 账户摘要（强平价 0.4226，距强平 29.8%，used_margin=584.81）
	if err := s.InsertCMCAccountSummary(ctx, pgstore.CMCAccountSummary{
		AccountID: 1, TotalAccountValue: 2169.02, TotalCashBalance: 2026.76,
		TotalEquityUsedInMargin: 584.81,
		CloseoutPrice:           0.4226, DistanceToCloseoutPct: 29.8, FetchedAt: now,
	}); err != nil {
		t.Fatalf("InsertCMCAccountSummary: %v", err)
	}

	// 未接线 cmcLiveProvider → freshLive 回退 DB positions 行（PnL=DB unrealised_pnl）
	svc := &Service{store: s, freshTTL: time.Minute, liqMMR: 0.005}
	resp, err := svc.GetHedgeStructure(ctx, connect.NewRequest(&v1.GetHedgeStructureRequest{}))
	if err != nil {
		t.Fatalf("GetHedgeStructure: %v", err)
	}
	st := resp.Msg.Structure
	if st == nil {
		t.Fatal("structure = nil")
	}
	// baseline + mid（DB 回退：mid=EvaluationBidPrice=0.32285，因 freshLive DB 回退用 bid）
	if st.BaselinePrice != 0.33171 {
		t.Errorf("baseline = %v, want 0.33171", st.BaselinePrice)
	}
	// D-236 变更9：空单 PnL=平台自报值（DB unrealised_pnl=-117.83，无镜像计算）
	if st.LegShort.PnlVsBase != -117.83 {
		t.Errorf("leg_short pnl = %v, want -117.83 (平台自报值, D-236 变更9)", st.LegShort.PnlVsBase)
	}
	// 质押腿 PnL 不再镜像计算（现货无对手方报价、无开仓成本记录，反推=虚构）→ 0（前端显示「—」）
	if st.LegSpot.PnlVsBase != 0 {
		t.Errorf("leg_spot pnl = %v, want 0 (D-236 变更9 退役镜像, 前端显示「—」)", st.LegSpot.PnlVsBase)
	}
	// leg_spot qty_staked=18070（v1.2 数量源 fin_trx_account）
	if st.LegSpot.QtyStaked != 18070 {
		t.Errorf("qty_staked = %v, want 18070 (fin_trx_account.votes_total)", st.LegSpot.QtyStaked)
	}
	// leg_short qty=18100（CMC position，freshLive DB 回退）
	if st.LegShort.Qty != 18100 {
		t.Errorf("leg_short qty = %v, want 18100 (CMC position)", st.LegShort.Qty)
	}
	// stake 明细（单位断言：_trx 原值）
	if st.LegSpot.Stake == nil {
		t.Fatal("stake = nil")
	}
	if st.LegSpot.Stake.Energy != 17270 {
		t.Errorf("stake.energy = %v, want 17270 (TRX 原值)", st.LegSpot.Stake.Energy)
	}
	if st.LegSpot.Stake.Bandwidth != 800 {
		t.Errorf("stake.bandwidth = %v, want 800 (TRX 原值)", st.LegSpot.Stake.Bandwidth)
	}
	// net_exposure 真实计算 (balance+staked−short_qty)×mid
	// D-236 变更9：mid 用流值（freshLive DB 回退=EvaluationBidPrice=0.32285）
	wantNetTrx := 97.863827 + 18070 - 18100
	if diff := st.NetExposureTrx - wantNetTrx; diff > 1e-6 || diff < -1e-6 {
		t.Errorf("net_exposure_trx = %v, want %v (balance+staked−short)", st.NetExposureTrx, wantNetTrx)
	}
	wantNetUsd := wantNetTrx * 0.32285
	if diff := st.NetExposureUsd - wantNetUsd; diff > 1e-4 || diff < -1e-4 {
		t.Errorf("net_exposure_usd = %v, want %v (D-236 变更9 流 Mid)", st.NetExposureUsd, wantNetUsd)
	}
	// 强平价 + 距强平%
	if st.LegShort.Liq != 0.4226 {
		t.Errorf("liq = %v, want 0.4226", st.LegShort.Liq)
	}
	if st.LegShort.DistPct != 29.8 {
		t.Errorf("dist_pct = %v, want 29.8", st.LegShort.DistPct)
	}
	// 双账户摘要
	if st.Accts == nil {
		t.Fatal("accts = nil")
	}
	if st.Accts.TrxBalance != 97.863827 {
		t.Errorf("trx_balance = %v, want 97.863827", st.Accts.TrxBalance)
	}
	if st.Accts.CmcCash != 2026.76 {
		t.Errorf("cmc_cash = %v, want 2026.76", st.Accts.CmcCash)
	}
}

// TestGetHedgeStructure_CarryHandCalc carry 手算对拍（近 7 日 + 累计 + 年化）。
func TestGetHedgeStructure_CarryHandCalc(t *testing.T) {
	s := hedgeTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// TRX 账户（18070 质押）
	if err := s.InsertTRXAccount(ctx, pgstore.TRXAccount{
		Address: "TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", VotesTotal: 18070,
		BalanceSun: 97863827, EnergyDelegated: 17270000000, BandwidthStaked: 800000000,
		FetchedAt: now,
	}); err != nil {
		t.Fatalf("InsertTRXAccount: %v", err)
	}
	// CMC 持仓 + price（mid=0.3236；D-236 变更9：mid 走 freshLive DB 回退=EvaluationBidPrice）
	if err := s.InsertCMCPositions(ctx, 1, []pgstore.CMCPosition{{
		AccountID: 1, InstrumentID: "X-AFQUL", Direction: "SHORT", Quantity: 18100,
		EntryPrice: 0.33171, MarginPercentage: 0.1,
		EvaluationBidPrice: 0.3236, EvaluationAskPrice: 0.3240, FetchedAt: now,
	}}); err != nil {
		t.Fatalf("InsertCMCPositions: %v", err)
	}
	if err := s.InsertCMCPrice(ctx, pgstore.CMCPrice{InstrumentID: "X-AFQUL", MidPrice: 0.3236, FetchedAt: now}); err != nil {
		t.Fatalf("InsertCMCPrice: %v", err)
	}
	// daily_income：2 天 energy=5+10=15, voting=0.1+0.2=0.3, interest=1.5+2.5=4.0
	day1 := now.Add(-1 * 24 * time.Hour).Truncate(24 * time.Hour)
	day2 := now.Truncate(24 * time.Hour)
	if err := s.AddDailyIncomeAuto(ctx, day1, 5.0, 0.1, 1.5, nil, nil, "auto"); err != nil {
		t.Fatalf("AddDailyIncomeAuto day1: %v", err)
	}
	if _, err := s.AddDailyIncome(ctx, day2, 10.0, 0.2, 2.5, nil, nil, "manual"); err != nil {
		t.Fatalf("AddDailyIncome day2: %v", err)
	}
	// CMC 隔夜息：2 笔 1.6+1.5=3.1
	if err := s.UpsertCMCHistory(ctx, 1, []pgstore.CMCHistoryTx{
		{AccountID: 1, TransactionID: "o1", TransactionType: "OVERNIGHT_SETTLEMENT", Amount: 1.6, TransactionTime: now.Add(-time.Hour)},
		{AccountID: 1, TransactionID: "o2", TransactionType: "OVERNIGHT_SETTLEMENT", Amount: 1.5, TransactionTime: now.Add(-2 * time.Hour)},
	}); err != nil {
		t.Fatalf("UpsertCMCHistory: %v", err)
	}

	svc := &Service{store: s, freshTTL: time.Minute, liqMMR: 0.005}
	resp, err := svc.GetHedgeStructure(ctx, connect.NewRequest(&v1.GetHedgeStructureRequest{}))
	if err != nil {
		t.Fatalf("GetHedgeStructure: %v", err)
	}
	carry := resp.Msg.Structure.Carry
	if carry == nil {
		t.Fatal("carry = nil")
	}
	// 近 7 日 energy=15, voting=0.3（2 天都在 7 日内）
	if carry.TrxRental_7DTrx != 15.0 {
		t.Errorf("trx_rental_7d = %v, want 15.0", carry.TrxRental_7DTrx)
	}
	if carry.TrxVote_7DTrx != 0.3 {
		t.Errorf("trx_vote_7d = %v, want 0.3", carry.TrxVote_7DTrx)
	}
	// 近 7 日隔夜息=3.1
	if carry.CmcOvernight_7DUsd != 3.1 {
		t.Errorf("cmc_overnight_7d = %v, want 3.1", carry.CmcOvernight_7DUsd)
	}
	// 累计 cum_usd = (15+0.3)×0.3236 + 4.0 = 4.950 + 4.0 = 8.950（近似）
	wantCum := (15.0+0.3)*0.3236 + 4.0
	if diff := carry.CumUsd - wantCum; diff > 1e-4 || diff < -1e-4 {
		t.Errorf("cum_usd = %v, want %v", carry.CumUsd, wantCum)
	}
	// 年化 = cum_usd / (18070×0.3236) × 365 × 100
	wantAnnual := wantCum / (18070 * 0.3236) * 365 * 100
	if diff := carry.AnnualizedPct - wantAnnual; diff > 1e-4 || diff < -1e-4 {
		t.Errorf("annualized_pct = %v, want %v", carry.AnnualizedPct, wantAnnual)
	}
}

// TestListHedgeFlow_RPC 统一流水卡 RPC（跨源交错+kind 归一+单位断言）。
func TestListHedgeFlow_RPC(t *testing.T) {
	s := hedgeTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	// TRX 2 条
	if err := s.UpsertTRXTx(ctx, pgstore.TRXTx{TxID: "t1", Ts: base, Kind: "rental_income", AmountSun: 5320000}); err != nil {
		t.Fatalf("UpsertTRXTx: %v", err)
	}
	if err := s.UpsertTRXTx(ctx, pgstore.TRXTx{TxID: "t2", Ts: base.Add(2 * time.Minute), Kind: "vote_claim", AmountSun: 0}); err != nil {
		t.Fatalf("UpsertTRXTx: %v", err)
	}
	// CMC 1 条 overnight + 1 条 SELL_TRADE→other
	if err := s.UpsertCMCHistory(ctx, 1, []pgstore.CMCHistoryTx{
		{AccountID: 1, TransactionID: "c1", TransactionType: "OVERNIGHT_SETTLEMENT", Amount: 1.6, TransactionTime: base.Add(time.Minute)},
		{AccountID: 1, TransactionID: "c2", TransactionType: "SELL_TRADE", Amount: 100, TransactionTime: base.Add(3 * time.Minute)},
	}); err != nil {
		t.Fatalf("UpsertCMCHistory: %v", err)
	}

	svc := &Service{store: s, freshTTL: time.Minute, liqMMR: 0.005}
	resp, err := svc.ListHedgeFlow(ctx, connect.NewRequest(&v1.ListHedgeFlowRequest{SourceFilter: "", Limit: 10, Offset: 0}))
	if err != nil {
		t.Fatalf("ListHedgeFlow: %v", err)
	}
	txs := resp.Msg.Txs
	if len(txs) != 4 {
		t.Fatalf("txs len = %d, want 4", len(txs))
	}
	// 倒序：c2(12:03) t2(12:02) c1(12:01) t1(12:00)
	wantOrder := []string{"c2", "t2", "c1", "t1"}
	for i, w := range wantOrder {
		if txs[i].TxId != w {
			t.Errorf("txs[%d].tx_id = %q, want %q", i, txs[i].TxId, w)
		}
	}
	// kind 归一：c2 SELL_TRADE→other，c1 OVERNIGHT→overnight
	if txs[0].Kind != "other" {
		t.Errorf("c2 kind = %q, want other (SELL_TRADE→other A5)", txs[0].Kind)
	}
	if txs[2].Kind != "overnight" {
		t.Errorf("c1 kind = %q, want overnight", txs[2].Kind)
	}
	// 单位断言：TRX 笔=TRX，CMC 笔=USD
	if txs[1].AmountUnit != "TRX" {
		t.Errorf("t2 unit = %q, want TRX", txs[1].AmountUnit)
	}
	if txs[0].AmountUnit != "USD" {
		t.Errorf("c2 unit = %q, want USD", txs[0].AmountUnit)
	}
	// source 断言
	if txs[1].Source != "trx" {
		t.Errorf("t2 source = %q, want trx", txs[1].Source)
	}
	if txs[0].Source != "cmc" {
		t.Errorf("c2 source = %q, want cmc", txs[0].Source)
	}
}

// TestGetHedgeStructure_EmptyTolerant 空表容忍（任一源空 → structure 非 nil 但字段零值）。
func TestGetHedgeStructure_EmptyTolerant(t *testing.T) {
	s := hedgeTestStore(t)
	ctx := context.Background()
	svc := &Service{store: s, freshTTL: time.Minute, liqMMR: 0.005}
	resp, err := svc.GetHedgeStructure(ctx, connect.NewRequest(&v1.GetHedgeStructureRequest{}))
	if err != nil {
		t.Fatalf("GetHedgeStructure empty: %v", err)
	}
	st := resp.Msg.Structure
	if st == nil {
		t.Fatal("structure = nil, want non-nil (空表容忍)")
	}
	if st.Mid != 0 {
		t.Errorf("mid = %v, want 0 (空表)", st.Mid)
	}
	if st.LegSpot == nil {
		t.Error("leg_spot = nil, want non-nil (空表容忍)")
	}
}

// TestGetHedgeStructure_WsConnected D-193-A/R9：cmcWsHealth seam true/false → WsConnected 断言。
// nil=未接线→false（黄点 REST 兜底）；true→绿点 WS 在线。
func TestGetHedgeStructure_WsConnected(t *testing.T) {
	s := hedgeTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	// 最小数据：TRX 账户 + CMC 持仓 + price（ready=true 才走组装路径）
	if err := s.InsertTRXAccount(ctx, pgstore.TRXAccount{
		Address: "TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", VotesTotal: 18070,
		BalanceSun: 97863827, AllowanceSun: 7717994, EnergyDelegated: 17270000000,
		BandwidthStaked: 800000000,
		FetchedAt:       now,
	}); err != nil {
		t.Fatalf("InsertTRXAccount: %v", err)
	}
	if err := s.InsertCMCPositions(ctx, 1, []pgstore.CMCPosition{{
		AccountID: 1, InstrumentID: "X-AFQUL", Direction: "SELL", Quantity: 18100,
		EntryPrice: 0.33171, FetchedAt: now,
	}}); err != nil {
		t.Fatalf("InsertCMCPositions: %v", err)
	}
	if err := s.InsertCMCPrice(ctx, pgstore.CMCPrice{InstrumentID: "X-AFQUL", MidPrice: 0.3236, FetchedAt: now}); err != nil {
		t.Fatalf("InsertCMCPrice: %v", err)
	}

	// case 1: nil=未接线→false
	svc1 := &Service{store: s, freshTTL: time.Minute, liqMMR: 0.005}
	resp1, err := svc1.GetHedgeStructure(ctx, connect.NewRequest(&v1.GetHedgeStructureRequest{}))
	if err != nil {
		t.Fatalf("GetHedgeStructure case1: %v", err)
	}
	if resp1.Msg.Structure.WsConnected {
		t.Error("nil seam: WsConnected = true, want false (未接线→REST 兜底)")
	}

	// case 2: true→绿点
	svc2 := &Service{store: s, freshTTL: time.Minute, liqMMR: 0.005, cmcWsHealth: func() bool { return true }}
	resp2, err := svc2.GetHedgeStructure(ctx, connect.NewRequest(&v1.GetHedgeStructureRequest{}))
	if err != nil {
		t.Fatalf("GetHedgeStructure case2: %v", err)
	}
	if !resp2.Msg.Structure.WsConnected {
		t.Error("true seam: WsConnected = false, want true (WS 在线)")
	}

	// case 3: false→黄点
	svc3 := &Service{store: s, freshTTL: time.Minute, liqMMR: 0.005, cmcWsHealth: func() bool { return false }}
	resp3, err := svc3.GetHedgeStructure(ctx, connect.NewRequest(&v1.GetHedgeStructureRequest{}))
	if err != nil {
		t.Fatalf("GetHedgeStructure case3: %v", err)
	}
	if resp3.Msg.Structure.WsConnected {
		t.Error("false seam: WsConnected = true, want false (REST 兜底)")
	}
}

// TestGetHedgeStructure_AskFreshEvalAskSameSource D-234 R-V2 → D-236 变更9：快照路径 Ask 走 freshLive（与 case13 推送同源）。
// 注入 cmcLiveProvider 返回新鲜内存值 → GetHedgeStructure 返回 structure.ask=内存值（不取 DB ForSide 门限值）。
func TestGetHedgeStructure_AskFreshEvalAskSameSource(t *testing.T) {
	s := hedgeTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := s.InsertTRXAccount(ctx, pgstore.TRXAccount{
		Address: "TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", VotesTotal: 18070,
		BalanceSun: 97863827, AllowanceSun: 7717994, EnergyDelegated: 17270000000,
		BandwidthStaked: 800000000,
		FetchedAt:       now,
	}); err != nil {
		t.Fatalf("InsertTRXAccount: %v", err)
	}
	if err := s.InsertCMCPositions(ctx, 1, []pgstore.CMCPosition{{
		AccountID: 1, InstrumentID: "X-AFQUL", Direction: "SELL", Quantity: 18100,
		EntryPrice: 0.33171, EvaluationAskPrice: 0.3000, EvaluationBidPrice: 0.2990,
		FetchedAt: now,
	}}); err != nil {
		t.Fatalf("InsertCMCPositions: %v", err)
	}
	// DB 写入旧 ask（0.3000）——若快照路径仍取 DB，ask=0.3000；内存值 0.3500 应胜出。
	if err := s.InsertCMCPrice(ctx, pgstore.CMCPrice{InstrumentID: "X-AFQUL", MidPrice: 0.3236, FetchedAt: now}); err != nil {
		t.Fatalf("InsertCMCPrice: %v", err)
	}

	// case 1: provider 注入新鲜内存值 0.3500 → structure.ask=0.3500（内存直通，不取 DB）
	memAsk := 0.3500
	svc1 := &Service{store: s, freshTTL: time.Minute, liqMMR: 0.005, cmcLiveProvider: func() cmc.LiveState {
		return cmc.LiveState{Ask: memAsk, Mid: memAsk, Qty: 18100, PosTs: now} // 新鲜（now，<30s）
	}}
	resp1, err := svc1.GetHedgeStructure(ctx, connect.NewRequest(&v1.GetHedgeStructureRequest{}))
	if err != nil {
		t.Fatalf("GetHedgeStructure case1: %v", err)
	}
	if got := resp1.Msg.Structure.Ask; got != memAsk {
		t.Errorf("fresh provider: Ask = %v, want %v (内存直通，与 case13 同源)", got, memAsk)
	}

	// case 2: provider 未接线（nil）→ 回退 DB positions 行（快照/推送同源回退路径）
	svc2 := &Service{store: s, freshTTL: time.Minute, liqMMR: 0.005}
	resp2, err := svc2.GetHedgeStructure(ctx, connect.NewRequest(&v1.GetHedgeStructureRequest{}))
	if err != nil {
		t.Fatalf("GetHedgeStructure case2: %v", err)
	}
	// DB 回退值非 0.3500（内存值）——证明 case1 的 0.3500 来自内存不是 DB
	if resp2.Msg.Structure.Ask == memAsk {
		t.Error("nil provider: Ask == memAsk, want DB fallback (证明 case1 内存直通非巧合)")
	}

	// case 3: provider 返回陈旧值（>30s）→ 回退 DB（与 case13 同源新鲜度门限）
	staleTs := now.Add(-31 * time.Second)
	svc3 := &Service{store: s, freshTTL: time.Minute, liqMMR: 0.005, cmcLiveProvider: func() cmc.LiveState {
		return cmc.LiveState{Ask: memAsk, Mid: memAsk, Qty: 18100, PosTs: staleTs} // 陈旧（>30s）
	}}
	resp3, err := svc3.GetHedgeStructure(ctx, connect.NewRequest(&v1.GetHedgeStructureRequest{}))
	if err != nil {
		t.Fatalf("GetHedgeStructure case3: %v", err)
	}
	if resp3.Msg.Structure.Ask == memAsk {
		t.Error("stale provider: Ask == memAsk, want DB fallback (>30s 回退，与 case13 同源门限)")
	}
}

// TestComputePendingReward D-233 S2：待领取 = allowance + Σ Vi × userVotes / 10^18。
// 验证：有 Vi 数据→待领取 > allowance；无 Vi 数据→回退纯 allowance。
func TestComputePendingReward(t *testing.T) {
	s := trxTestStore(t)
	ctx := context.Background()

	// 无 Vi 数据 → 回退纯 allowance
	got := computePendingReward(ctx, s, 7717994) // 7.717994 TRX
	if got <= 0 {
		t.Errorf("no Vi fallback: got %v, want >0 (pure allowance)", got)
	}
	// 应约等于 7.717994（纯 allowance，无 Vi）
	if got < 7.7 || got > 7.8 {
		t.Errorf("no Vi fallback: got %v, want ~7.717994 (pure allowance)", got)
	}

	// 写 Vi 数据：1 SR，Vi=10^18（即 1 sun/vote/cycle），userVotes=10000
	// unsettled = 10^18 × 10000 / 10^18 = 10000 sun = 0.01 TRX
	vi := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	if err := s.UpsertTRXCycleVi(ctx, pgstore.TRXCycleVi{
		Cycle:     1,
		SRAddress: "SR_A_ADDR",
		Vi:        vi,
		UserVotes: 10000,
		UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("UpsertTRXCycleVi: %v", err)
	}

	got2 := computePendingReward(ctx, s, 7717994) // 7.717994 + 0.01 = 7.727994
	if got2 <= 7.717994 {
		t.Errorf("with Vi: got %v, want > 7.717994 (allowance + unsettled)", got2)
	}
	// 应约等于 7.727994
	if got2 < 7.72 || got2 > 7.74 {
		t.Errorf("with Vi: got %v, want ~7.727994 (allowance + 0.01 unsettled)", got2)
	}
}
