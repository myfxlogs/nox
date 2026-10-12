// D-258 S3 测试：GetTrxYieldRadar RPC handler（收益率雷达）。
package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	v1 "nox/internal/gen/dashboardv1"
	"nox/internal/gen/dashboardv1/dashboardv1connect"

	"nox/internal/data/bus"
	"nox/internal/store/pgstore"

	"connectrpc.com/connect"
)

// TestGetTrxYieldRadar_FullData 全量数据 → 各字段正确填充。
// 7d rental 各 5 TRX（5000000 sun）= 35 TRX；在押 17270 TRX → APR ≈ 10.57%。
// 投票 APR：current_rate=3.29，30d 均值=3.20。
func TestGetTrxYieldRadar_FullData(t *testing.T) {
	s := trxTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	// 7d rental（每天 5 TRX）
	for i := 6; i >= 0; i-- {
		ts := now.Add(-time.Duration(i) * 24 * time.Hour)
		if err := s.UpsertTRXTx(ctx, pgstore.TRXTx{
			TxID: "radar_" + ts.Format("20060102"), Ts: ts,
			Kind: "rental_income", AmountSun: 5000000,
		}); err != nil {
			t.Fatalf("UpsertTRXTx %d: %v", i, err)
		}
	}
	// 账户快照
	if err := s.InsertTRXAccount(ctx, pgstore.TRXAccount{
		Address: "TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", EnergyDelegated: 17270000000, FetchedAt: now,
	}); err != nil {
		t.Fatalf("InsertTRXAccount: %v", err)
	}
	// 投票 APR：当前 + 30d 历史（当前时间戳用 now+1s 确保是最新）
	if err := s.InsertTRXVoteApr(ctx, pgstore.TRXVoteApr{
		FetchedAt: now.Add(time.Second), CurrentRate: 3.29, MaxRate: 4.0, Spread: 0.71,
	}); err != nil {
		t.Fatalf("InsertTRXVoteApr current: %v", err)
	}
	for i := 29; i >= 0; i-- {
		ts := now.Add(-time.Duration(i) * 24 * time.Hour)
		if err := s.InsertTRXVoteApr(ctx, pgstore.TRXVoteApr{
			FetchedAt: ts, CurrentRate: 3.20, MaxRate: 4.0, Spread: 0.80,
		}); err != nil {
			t.Fatalf("InsertTRXVoteApr %d: %v", i, err)
		}
	}
	qbus := bus.New()
	svc := &Service{store: s, qbus: qbus, freshTTL: time.Minute, liqMMR: 0.005, trxFloorPct: 8.0}
	resp, err := svc.GetTrxYieldRadar(ctx, connect.NewRequest(&v1.GetTrxYieldRadarRequest{}))
	if err != nil {
		t.Fatalf("GetTrxYieldRadar: %v", err)
	}
	r := resp.Msg
	// 能量 7d APR ≈ 10.57%
	if r.EnergyApr_7D < 10.0 || r.EnergyApr_7D > 11.0 {
		t.Errorf("EnergyApr_7D = %.2f, want ~10.57", r.EnergyApr_7D)
	}
	if r.SampleDays_7D < 7 {
		t.Errorf("SampleDays_7D = %d, want >= 7", r.SampleDays_7D)
	}
	// 投票当前 APR = 3.29
	if r.VoteAprCurrent != 3.29 {
		t.Errorf("VoteAprCurrent = %v, want 3.29", r.VoteAprCurrent)
	}
	// D-288：市场最高 APR 透传（MaxRate=4.0, Spread=0.71）
	if r.VoteAprMax != 4.0 {
		t.Errorf("VoteAprMax = %v, want 4.0", r.VoteAprMax)
	}
	if r.VoteAprSpread != 0.71 {
		t.Errorf("VoteAprSpread = %v, want 0.71", r.VoteAprSpread)
	}
	// 投票 30d 均值 ≈ 3.20
	if r.VoteApr_30DAvg < 3.0 || r.VoteApr_30DAvg > 3.4 {
		t.Errorf("VoteApr_30DAvg = %.4f, want ~3.20", r.VoteApr_30DAvg)
	}
	// floor = 8.0
	if r.FloorPct != 8.0 {
		t.Errorf("FloorPct = %v, want 8.0", r.FloorPct)
	}
	// updated_at 非零
	if r.UpdatedAt == nil || r.UpdatedAt.AsTime().IsZero() {
		t.Error("UpdatedAt should be non-zero")
	}
}

// TestGetTrxYieldRadar_Empty 空表 → 全零响应，不报错。
func TestGetTrxYieldRadar_Empty(t *testing.T) {
	s := trxTestStore(t)
	ctx := context.Background()
	qbus := bus.New()
	svc := &Service{store: s, qbus: qbus, freshTTL: time.Minute, liqMMR: 0.005, trxFloorPct: 8.0}
	resp, err := svc.GetTrxYieldRadar(ctx, connect.NewRequest(&v1.GetTrxYieldRadarRequest{}))
	if err != nil {
		t.Fatalf("GetTrxYieldRadar empty: %v", err)
	}
	r := resp.Msg
	if r.EnergyApr_7D != 0 || r.EnergyApr_30D != 0 {
		t.Errorf("empty table APR should be 0, got 7d=%v 30d=%v", r.EnergyApr_7D, r.EnergyApr_30D)
	}
	if r.VoteAprCurrent != 0 || r.VoteApr_30DAvg != 0 {
		t.Errorf("empty table vote APR should be 0, got current=%v avg=%v", r.VoteAprCurrent, r.VoteApr_30DAvg)
	}
	if r.FloorPct != 8.0 {
		t.Errorf("FloorPct = %v, want 8.0 (config 注入)", r.FloorPct)
	}
}

// TestGetTrxYieldRadar_HttpSmoke HTTP smoke：通过 ConnectRPC 客户端调用。
func TestGetTrxYieldRadar_HttpSmoke(t *testing.T) {
	s := trxTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	// 最少数据：1 条投票 APR
	if err := s.InsertTRXVoteApr(ctx, pgstore.TRXVoteApr{
		FetchedAt: now, CurrentRate: 3.29, MaxRate: 4.0, Spread: 0.71,
	}); err != nil {
		t.Fatalf("InsertTRXVoteApr: %v", err)
	}
	qbus := bus.New()
	svc := &Service{store: s, qbus: qbus, freshTTL: time.Minute, liqMMR: 0.005, trxFloorPct: 8.0}
	path, handler := dashboardv1connect.NewDashboardServiceHandler(svc)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := dashboardv1connect.NewDashboardServiceClient(http.DefaultClient, srv.URL)
	resp, err := client.GetTrxYieldRadar(ctx, connect.NewRequest(&v1.GetTrxYieldRadarRequest{}))
	if err != nil {
		t.Fatalf("HTTP GetTrxYieldRadar: %v", err)
	}
	if resp.Msg.VoteAprCurrent != 3.29 {
		t.Errorf("VoteAprCurrent = %v, want 3.29", resp.Msg.VoteAprCurrent)
	}
	if resp.Msg.FloorPct != 8.0 {
		t.Errorf("FloorPct = %v, want 8.0", resp.Msg.FloorPct)
	}
}
