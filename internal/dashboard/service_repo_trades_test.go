// D-272 S8 测试：Dashboard RPC handler（DSN 真实夹具）。
package dashboard

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	"arb/internal/testutil/storetest"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
)

// repoTradesSvcTest 连接 ARB_TEST_PG_DSN，构造 Service + 清表。
func repoTradesSvcTest(t *testing.T) *Service {
	t.Helper()
	st := storetest.SharedStore(t, "ARB_TEST_PG_DSN")
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx, "TRUNCATE cnfi_repo_trades, cnfi_repo_ops, alerts, fin_repo_rates, cnfi_repo_config CASCADE"); err != nil {
		t.Fatalf("TRUNCATE: %v", err)
	}
	// reseed cnfi_repo_config
	if _, err := st.Pool().Exec(ctx, `
		INSERT INTO cnfi_repo_config (id, idle_cny, min_return_cny, reminder_hour, enabled)
		VALUES (1, 50000, 5, 10, true) ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatalf("reseed cnfi_repo_config: %v", err)
	}
	return &Service{store: st}
}

// TestRecordRepoTrade_Success 交易时段录入 → 回显理论值+偏差。
func TestRecordRepoTrade_Success(t *testing.T) {
	s := repoTradesSvcTest(t)
	ctx := context.Background()
	// 种子利率
	tradeDate := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	if err := s.store.InsertRepoRate(ctx, "204001", "GC001", 1.45, nil, tradeDate); err != nil {
		t.Fatalf("InsertRepoRate: %v", err)
	}
	resp, err := s.RecordRepoTrade(ctx, connect.NewRequest(&v1.RecordRepoTradeRequest{
		InstrumentCode: "204001",
		TradeDate:      "2026-09-10",
		HoldingDays:    3,
		PrincipalCny:   400000,
		ActualRatePct:  1.45,
	}))
	if err != nil {
		t.Fatalf("RecordRepoTrade: %v", err)
	}
	if !resp.Msg.Ok {
		t.Fatalf("ok=false, error=%s", resp.Msg.Error)
	}
	if resp.Msg.TheoreticalRatePct == nil || *resp.Msg.TheoreticalRatePct != 1.45 {
		t.Errorf("TheoreticalRatePct = %v, want 1.45", resp.Msg.TheoreticalRatePct)
	}
	// theoretical_return_cny = 1.45/100 * 400000 * 3/365 = 47.67
	if resp.Msg.TheoreticalReturnCny == nil {
		t.Error("TheoreticalReturnCny = nil, want 47.67")
	} else if *resp.Msg.TheoreticalReturnCny < 47.0 || *resp.Msg.TheoreticalReturnCny > 48.0 {
		t.Errorf("TheoreticalReturnCny = %v, want ~47.67", *resp.Msg.TheoreticalReturnCny)
	}
	// deviation_rate_bps = (1.45 - 1.45) * 100 = 0
	if resp.Msg.DeviationRateBps == nil || *resp.Msg.DeviationRateBps != 0 {
		t.Errorf("DeviationRateBps = %v, want 0", resp.Msg.DeviationRateBps)
	}
	// deviation_principal = 400000 - 50000 = 350000
	if resp.Msg.DeviationPrincipalCny != 350000 {
		t.Errorf("DeviationPrincipalCny = %v, want 350000", resp.Msg.DeviationPrincipalCny)
	}
	// maturity_date 默认 = trade_date+1 = 2026-09-11
	if resp.Msg.MaturityDate != "2026-09-11" {
		t.Errorf("MaturityDate = %q, want 2026-09-11", resp.Msg.MaturityDate)
	}
}

// TestRecordRepoTrade_NonTradingHours 非交易时段（无利率数据）→ 理论值 NULL，仍可录入。
func TestRecordRepoTrade_NonTradingHours(t *testing.T) {
	s := repoTradesSvcTest(t)
	resp, err := s.RecordRepoTrade(context.Background(), connect.NewRequest(&v1.RecordRepoTradeRequest{
		InstrumentCode: "204001",
		TradeDate:      "2026-09-10",
		HoldingDays:    3,
		PrincipalCny:   400000,
		ActualRatePct:  1.45,
	}))
	if err != nil {
		t.Fatalf("RecordRepoTrade: %v", err)
	}
	if !resp.Msg.Ok {
		t.Fatalf("ok=false, error=%s", resp.Msg.Error)
	}
	if resp.Msg.TheoreticalRatePct != nil {
		t.Errorf("TheoreticalRatePct = %v, want nil (非交易时段)", resp.Msg.TheoreticalRatePct)
	}
	if resp.Msg.TheoreticalReturnCny != nil {
		t.Errorf("TheoreticalReturnCny = %v, want nil (非交易时段)", resp.Msg.TheoreticalReturnCny)
	}
	if resp.Msg.DeviationRateBps != nil {
		t.Errorf("DeviationRateBps = %v, want nil (非交易时段)", resp.Msg.DeviationRateBps)
	}
}

// TestRecordRepoTrade_Duplicate 同日同品种第二次录入 → error。
func TestRecordRepoTrade_Duplicate(t *testing.T) {
	s := repoTradesSvcTest(t)
	ctx := context.Background()
	req := &v1.RecordRepoTradeRequest{
		InstrumentCode: "204001", TradeDate: "2026-09-10",
		HoldingDays: 3, PrincipalCny: 400000, ActualRatePct: 1.45,
	}
	if _, err := s.RecordRepoTrade(ctx, connect.NewRequest(req)); err != nil {
		t.Fatalf("first RecordRepoTrade: %v", err)
	}
	resp, err := s.RecordRepoTrade(ctx, connect.NewRequest(req))
	if err != nil {
		t.Fatalf("second RecordRepoTrade: %v", err)
	}
	if resp.Msg.Ok {
		t.Error("second RecordRepoTrade ok=true, want false (duplicate)")
	}
	if resp.Msg.Error == "" {
		t.Error("Error empty, want duplicate message")
	}
}

// TestRecordRepoTrade_MaturityOverride 业主覆盖到期日。
func TestRecordRepoTrade_MaturityOverride(t *testing.T) {
	s := repoTradesSvcTest(t)
	md := "2026-09-13"
	resp, err := s.RecordRepoTrade(context.Background(), connect.NewRequest(&v1.RecordRepoTradeRequest{
		InstrumentCode: "204001", TradeDate: "2026-09-10",
		HoldingDays: 3, PrincipalCny: 400000, ActualRatePct: 1.45,
		MaturityDate: &md,
	}))
	if err != nil {
		t.Fatalf("RecordRepoTrade: %v", err)
	}
	if !resp.Msg.Ok {
		t.Fatalf("ok=false, error=%s", resp.Msg.Error)
	}
	if resp.Msg.MaturityDate != "2026-09-13" {
		t.Errorf("MaturityDate = %q, want 2026-09-13 (owner override)", resp.Msg.MaturityDate)
	}
}

// TestRecordRepoTrade_MaturityBeforeTrade 业主覆盖到期日早于操作日 → error。
func TestRecordRepoTrade_MaturityBeforeTrade(t *testing.T) {
	s := repoTradesSvcTest(t)
	md := "2026-09-09"
	resp, err := s.RecordRepoTrade(context.Background(), connect.NewRequest(&v1.RecordRepoTradeRequest{
		InstrumentCode: "204001", TradeDate: "2026-09-10",
		HoldingDays: 3, PrincipalCny: 400000, ActualRatePct: 1.45,
		MaturityDate: &md,
	}))
	if err != nil {
		t.Fatalf("RecordRepoTrade: %v", err)
	}
	if resp.Msg.Ok {
		t.Error("ok=true, want false (maturity_date < trade_date)")
	}
}

// TestSettleRepoTrade_Success 结算 → 偏差计算。
func TestSettleRepoTrade_Success(t *testing.T) {
	s := repoTradesSvcTest(t)
	ctx := context.Background()
	// 先录入（带理论收益）
	tradeDate := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	s.store.InsertRepoRate(ctx, "204001", "GC001", 1.45, nil, tradeDate)
	recResp, err := s.RecordRepoTrade(ctx, connect.NewRequest(&v1.RecordRepoTradeRequest{
		InstrumentCode: "204001", TradeDate: "2026-09-10",
		HoldingDays: 3, PrincipalCny: 400000, ActualRatePct: 1.45,
	}))
	if err != nil || !recResp.Msg.Ok {
		t.Fatalf("RecordRepoTrade: err=%v ok=%v", err, recResp.Msg.Ok)
	}
	// 结算
	resp, err := s.SettleRepoTrade(ctx, connect.NewRequest(&v1.SettleRepoTradeRequest{
		Id: recResp.Msg.Id, ActualReturnCny: 50.0,
	}))
	if err != nil {
		t.Fatalf("SettleRepoTrade: %v", err)
	}
	if !resp.Msg.Ok {
		t.Fatalf("ok=false, error=%s", resp.Msg.Error)
	}
	if resp.Msg.DeviationReturnCny == nil {
		t.Error("DeviationReturnCny = nil, want non-nil")
	}
}

// TestSettleRepoTrade_AlreadySettled 已结算再结算 → error。
func TestSettleRepoTrade_AlreadySettled(t *testing.T) {
	s := repoTradesSvcTest(t)
	ctx := context.Background()
	tradeDate := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	s.store.InsertRepoRate(ctx, "204001", "GC001", 1.45, nil, tradeDate)
	recResp, _ := s.RecordRepoTrade(ctx, connect.NewRequest(&v1.RecordRepoTradeRequest{
		InstrumentCode: "204001", TradeDate: "2026-09-10",
		HoldingDays: 3, PrincipalCny: 400000, ActualRatePct: 1.45,
	}))
	s.SettleRepoTrade(ctx, connect.NewRequest(&v1.SettleRepoTradeRequest{
		Id: recResp.Msg.Id, ActualReturnCny: 50.0,
	}))
	// 再结算
	resp, err := s.SettleRepoTrade(ctx, connect.NewRequest(&v1.SettleRepoTradeRequest{
		Id: recResp.Msg.Id, ActualReturnCny: 60.0,
	}))
	if err != nil {
		t.Fatalf("SettleRepoTrade: %v", err)
	}
	if resp.Msg.Ok {
		t.Error("ok=true, want false (already settled)")
	}
}

// TestDeleteRepoTrade_OpenCanDelete open 态可删+删后可重录。
func TestDeleteRepoTrade_OpenCanDelete(t *testing.T) {
	s := repoTradesSvcTest(t)
	ctx := context.Background()
	recResp, _ := s.RecordRepoTrade(ctx, connect.NewRequest(&v1.RecordRepoTradeRequest{
		InstrumentCode: "204001", TradeDate: "2026-09-10",
		HoldingDays: 3, PrincipalCny: 400000, ActualRatePct: 1.45,
	}))
	// 删除
	delResp, err := s.DeleteRepoTrade(ctx, connect.NewRequest(&v1.DeleteRepoTradeRequest{
		Id: recResp.Msg.Id,
	}))
	if err != nil {
		t.Fatalf("DeleteRepoTrade: %v", err)
	}
	if !delResp.Msg.Ok {
		t.Errorf("ok=false, error=%s", delResp.Msg.Error)
	}
	// 删后可重录
	recResp2, err := s.RecordRepoTrade(ctx, connect.NewRequest(&v1.RecordRepoTradeRequest{
		InstrumentCode: "204001", TradeDate: "2026-09-10",
		HoldingDays: 3, PrincipalCny: 400000, ActualRatePct: 1.50,
	}))
	if err != nil || !recResp2.Msg.Ok {
		t.Errorf("re-record after delete: err=%v ok=%v error=%s", err, recResp2.Msg.Ok, recResp2.Msg.Error)
	}
}

// TestDeleteRepoTrade_SettledCannotDelete settled 态不可删。
func TestDeleteRepoTrade_SettledCannotDelete(t *testing.T) {
	s := repoTradesSvcTest(t)
	ctx := context.Background()
	tradeDate := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	s.store.InsertRepoRate(ctx, "204001", "GC001", 1.45, nil, tradeDate)
	recResp, _ := s.RecordRepoTrade(ctx, connect.NewRequest(&v1.RecordRepoTradeRequest{
		InstrumentCode: "204001", TradeDate: "2026-09-10",
		HoldingDays: 3, PrincipalCny: 400000, ActualRatePct: 1.45,
	}))
	s.SettleRepoTrade(ctx, connect.NewRequest(&v1.SettleRepoTradeRequest{
		Id: recResp.Msg.Id, ActualReturnCny: 50.0,
	}))
	delResp, err := s.DeleteRepoTrade(ctx, connect.NewRequest(&v1.DeleteRepoTradeRequest{
		Id: recResp.Msg.Id,
	}))
	if err != nil {
		t.Fatalf("DeleteRepoTrade: %v", err)
	}
	if delResp.Msg.Ok {
		t.Error("ok=true, want false (settled cannot delete)")
	}
}

// TestListRepoTrades_InstrumentName 列表含 instrument_name 映射。
func TestListRepoTrades_InstrumentName(t *testing.T) {
	s := repoTradesSvcTest(t)
	ctx := context.Background()
	s.RecordRepoTrade(ctx, connect.NewRequest(&v1.RecordRepoTradeRequest{
		InstrumentCode: "204001", TradeDate: "2026-09-10",
		HoldingDays: 3, PrincipalCny: 400000, ActualRatePct: 1.45,
	}))
	resp, err := s.ListRepoTrades(ctx, connect.NewRequest(&v1.ListRepoTradesRequest{Limit: 10}))
	if err != nil {
		t.Fatalf("ListRepoTrades: %v", err)
	}
	if len(resp.Msg.Trades) != 1 {
		t.Fatalf("len = %d, want 1", len(resp.Msg.Trades))
	}
	if resp.Msg.Trades[0].InstrumentName != "GC001" {
		t.Errorf("InstrumentName = %q, want GC001", resp.Msg.Trades[0].InstrumentName)
	}
}

// TestGetRepoStrategyValidation 策略验证总览。
func TestGetRepoStrategyValidation(t *testing.T) {
	s := repoTradesSvcTest(t)
	ctx := context.Background()
	tradeDate := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	s.store.InsertRepoRate(ctx, "204001", "GC001", 1.45, nil, tradeDate)
	recResp, _ := s.RecordRepoTrade(ctx, connect.NewRequest(&v1.RecordRepoTradeRequest{
		InstrumentCode: "204001", TradeDate: "2026-09-10",
		HoldingDays: 3, PrincipalCny: 400000, ActualRatePct: 1.45,
	}))
	s.SettleRepoTrade(ctx, connect.NewRequest(&v1.SettleRepoTradeRequest{
		Id: recResp.Msg.Id, ActualReturnCny: 50.0,
	}))
	resp, err := s.GetRepoStrategyValidation(ctx, connect.NewRequest(&v1.GetRepoStrategyValidationRequest{}))
	if err != nil {
		t.Fatalf("GetRepoStrategyValidation: %v", err)
	}
	if resp.Msg.TotalTrades != 1 {
		t.Errorf("TotalTrades = %d, want 1", resp.Msg.TotalTrades)
	}
	if resp.Msg.SettledTrades != 1 {
		t.Errorf("SettledTrades = %d, want 1", resp.Msg.SettledTrades)
	}
	if resp.Msg.TotalActualReturnCny != 50.0 {
		t.Errorf("TotalActualReturnCny = %v, want 50.0", resp.Msg.TotalActualReturnCny)
	}
}

// D-276 测试：RecordRepoTrade 回显 fee_cny + net_theoretical_return_cny。
func TestRecordRepoTrade_FeeEcho(t *testing.T) {
	s := repoTradesSvcTest(t)
	ctx := context.Background()
	tradeDate := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	s.store.InsertRepoRate(ctx, "204001", "GC001", 1.385, nil, tradeDate)
	resp, err := s.RecordRepoTrade(ctx, connect.NewRequest(&v1.RecordRepoTradeRequest{
		InstrumentCode: "204001", TradeDate: "2026-09-10",
		HoldingDays: 1, PrincipalCny: 407000, ActualRatePct: 1.385,
	}))
	if err != nil {
		t.Fatalf("RecordRepoTrade: %v", err)
	}
	if !resp.Msg.Ok {
		t.Fatalf("ok=false, error=%s", resp.Msg.Error)
	}
	// fee_cny = 407000 × 0.00001 = 4.07
	expectedFee := 407000.0 * 0.00001
	if resp.Msg.FeeCny != expectedFee {
		t.Errorf("FeeCny = %v, want %v", resp.Msg.FeeCny, expectedFee)
	}
	// net_theoretical = theoretical - fee
	if resp.Msg.TheoreticalReturnCny == nil {
		t.Fatal("TheoreticalReturnCny = nil, want non-nil")
	}
	expectedNet := *resp.Msg.TheoreticalReturnCny - expectedFee
	if resp.Msg.NetTheoreticalReturnCny == nil {
		t.Fatal("NetTheoreticalReturnCny = nil, want non-nil")
	}
	if *resp.Msg.NetTheoreticalReturnCny != expectedNet {
		t.Errorf("NetTheoreticalReturnCny = %v, want %v", *resp.Msg.NetTheoreticalReturnCny, expectedNet)
	}
}

// D-276 测试：SettleRepoTrade 净口径偏差（actual=42.26, net_theoretical=42.26 → deviation=0）。
func TestSettleRepoTrade_NetDeviation(t *testing.T) {
	s := repoTradesSvcTest(t)
	ctx := context.Background()
	tradeDate := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	// r=1.385%, 1 day, principal=407000 → theoretical = 1.385/100 * 407000 * 1/365 = 15.443...
	// fee = 407000 * 0.00001 = 4.07
	// net_theoretical = theoretical - fee
	s.store.InsertRepoRate(ctx, "204001", "GC001", 1.385, nil, tradeDate)
	recResp, _ := s.RecordRepoTrade(ctx, connect.NewRequest(&v1.RecordRepoTradeRequest{
		InstrumentCode: "204001", TradeDate: "2026-09-10",
		HoldingDays: 1, PrincipalCny: 407000, ActualRatePct: 1.385,
	}))
	if !recResp.Msg.Ok {
		t.Fatalf("RecordRepoTrade failed: %s", recResp.Msg.Error)
	}
	// 验收锚：407000 GC001 → fee=4.07
	if recResp.Msg.FeeCny != 4.07 {
		t.Errorf("FeeCny = %v, want 4.07", recResp.Msg.FeeCny)
	}
	// 结算：actual = net_theoretical → deviation = 0
	if recResp.Msg.NetTheoreticalReturnCny == nil {
		t.Fatal("NetTheoreticalReturnCny = nil")
	}
	actualReturn := *recResp.Msg.NetTheoreticalReturnCny
	resp, err := s.SettleRepoTrade(ctx, connect.NewRequest(&v1.SettleRepoTradeRequest{
		Id: recResp.Msg.Id, ActualReturnCny: actualReturn,
	}))
	if err != nil {
		t.Fatalf("SettleRepoTrade: %v", err)
	}
	if !resp.Msg.Ok {
		t.Fatalf("ok=false, error=%s", resp.Msg.Error)
	}
	// 净口径偏差 = actual - net_theoretical = 0
	if resp.Msg.DeviationReturnCny == nil {
		t.Fatal("DeviationReturnCny = nil, want 0.0")
	}
	if *resp.Msg.DeviationReturnCny > 0.01 || *resp.Msg.DeviationReturnCny < -0.01 {
		t.Errorf("DeviationReturnCny = %v, want ~0 (净口径闭环)", *resp.Msg.DeviationReturnCny)
	}
}

// D-276 测试：ListRepoTrades 回显 fee_cny + net_theoretical_return_cny。
func TestListRepoTrades_FeeFields(t *testing.T) {
	s := repoTradesSvcTest(t)
	ctx := context.Background()
	s.store.InsertRepoRate(ctx, "204001", "GC001", 1.385, nil, time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC))
	s.RecordRepoTrade(ctx, connect.NewRequest(&v1.RecordRepoTradeRequest{
		InstrumentCode: "204001", TradeDate: "2026-09-10",
		HoldingDays: 1, PrincipalCny: 407000, ActualRatePct: 1.385,
	}))
	resp, err := s.ListRepoTrades(ctx, connect.NewRequest(&v1.ListRepoTradesRequest{Limit: 10}))
	if err != nil {
		t.Fatalf("ListRepoTrades: %v", err)
	}
	if len(resp.Msg.Trades) != 1 {
		t.Fatalf("len = %d, want 1", len(resp.Msg.Trades))
	}
	row := resp.Msg.Trades[0]
	if row.FeeCny == nil {
		t.Error("FeeCny = nil, want 4.07")
	} else if *row.FeeCny != 4.07 {
		t.Errorf("FeeCny = %v, want 4.07", *row.FeeCny)
	}
	if row.NetTheoreticalReturnCny == nil {
		t.Error("NetTheoreticalReturnCny = nil, want non-nil")
	}
}

// D-276 测试：GetRepoStrategyValidation 含 fee 聚合字段。
func TestGetRepoStrategyValidation_FeeAggregation(t *testing.T) {
	s := repoTradesSvcTest(t)
	ctx := context.Background()
	tradeDate := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	s.store.InsertRepoRate(ctx, "204001", "GC001", 1.385, nil, tradeDate)
	recResp, _ := s.RecordRepoTrade(ctx, connect.NewRequest(&v1.RecordRepoTradeRequest{
		InstrumentCode: "204001", TradeDate: "2026-09-10",
		HoldingDays: 1, PrincipalCny: 407000, ActualRatePct: 1.385,
	}))
	s.SettleRepoTrade(ctx, connect.NewRequest(&v1.SettleRepoTradeRequest{
		Id: recResp.Msg.Id, ActualReturnCny: 42.26,
	}))
	resp, err := s.GetRepoStrategyValidation(ctx, connect.NewRequest(&v1.GetRepoStrategyValidationRequest{}))
	if err != nil {
		t.Fatalf("GetRepoStrategyValidation: %v", err)
	}
	if resp.Msg.TotalFeeCny != 4.07 {
		t.Errorf("TotalFeeCny = %v, want 4.07", resp.Msg.TotalFeeCny)
	}
	// net = theoretical - fee
	if resp.Msg.TotalTheoreticalReturnCny == 0 {
		t.Error("TotalTheoreticalReturnCny = 0, want non-zero")
	}
	expectedNet := resp.Msg.TotalTheoreticalReturnCny - resp.Msg.TotalFeeCny
	if resp.Msg.TotalNetTheoreticalReturnCny != expectedNet {
		t.Errorf("TotalNetTheoreticalReturnCny = %v, want %v", resp.Msg.TotalNetTheoreticalReturnCny, expectedNet)
	}
}
