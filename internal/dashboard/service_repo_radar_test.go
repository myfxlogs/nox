// D-266 S3 测试：GetRepoRateRadar RPC handler（逆回购利率雷达）。
package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"arb/internal/dashboard/gen/arb/dashboard/v1/dashboardv1connect"

	"arb/internal/bus"
	"arb/internal/store/pgstore"

	"connectrpc.com/connect"
)

// repoRadarTestStore 连接 ARB_TEST_PG_SERVICE_DSN，TRUNCATE fin_repo_rates 隔离数据。
func repoRadarTestStore(t *testing.T) *pgstore.Store {
	t.Helper()
	s := serviceTestStoreSingleInit(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, "TRUNCATE fin_repo_rates CASCADE"); err != nil {
		t.Fatalf("TRUNCATE fin_repo_rates: %v", err)
	}
	return s
}

// seedRepoRates 插入 4 品种最新行 + GC007 30d 历史。
func seedRepoRates(t *testing.T, s *pgstore.Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	// 4 品种最新行
	for _, r := range []struct {
		code, name string
		rate       float64
	}{
		{"204001", "GC001", 1.375},
		{"204007", "GC007", 1.405},
		{"204014", "GC014", 1.415},
		{"204028", "GC028", 1.430},
	} {
		if err := s.InsertRepoRate(ctx, r.code, r.name, r.rate, ptrFloat(2), now); err != nil {
			t.Fatalf("InsertRepoRate %s: %v", r.name, err)
		}
	}
	// GC007 30d 历史（30 行，每天 1 行，rate=2.0）
	for i := 29; i >= 0; i-- {
		ts := now.Add(-time.Duration(i) * 24 * time.Hour)
		if err := s.InsertRepoRate(ctx, "204007", "GC007", 2.0, nil, ts); err != nil {
			t.Fatalf("InsertRepoRate history %d: %v", i, err)
		}
	}
}

// TestGetRepoRateRadar_FullData 全量数据 → 各字段正确填充。
func TestGetRepoRateRadar_FullData(t *testing.T) {
	s := repoRadarTestStore(t)
	seedRepoRates(t, s)
	ctx := context.Background()
	qbus := bus.New()
	svc := &Service{store: s, qbus: qbus, freshTTL: time.Minute, liqMMR: 0.005}
	resp, err := svc.GetRepoRateRadar(ctx, connect.NewRequest(&v1.GetRepoRateRadarRequest{}))
	if err != nil {
		t.Fatalf("GetRepoRateRadar: %v", err)
	}
	r := resp.Msg
	// 4 品种
	if len(r.Rates) != 4 {
		t.Fatalf("Rates len = %d, want 4", len(r.Rates))
	}
	// GC007 30d 均值 ≈ 2.0（30 行历史 rate=2.0 + 1 行最新 rate=1.405 → 均值略低于 2.0）
	if r.Gc007_30DAvg < 1.5 || r.Gc007_30DAvg > 2.5 {
		t.Errorf("Gc007_30DAvg = %.4f, want ~2.0", r.Gc007_30DAvg)
	}
	// OHLC 非零（有当日数据）
	if r.Gc007High <= 0 {
		t.Errorf("Gc007High = %v, want > 0 (有当日数据)", r.Gc007High)
	}
	// updated_at 非零
	if r.UpdatedAt == nil || r.UpdatedAt.AsTime().IsZero() {
		t.Error("UpdatedAt should be non-zero")
	}
	// R4-2：UpdatedAt 应反映存储的最新 ts，而非 RPC 请求时间（now）
	// 取 DB 中真实 max(ts) 对照，断言 UpdatedAt == max(ts)（非 now）
	var maxTs time.Time
	if err := s.Pool().QueryRow(ctx,
		`SELECT max(ts) FROM fin_repo_rates`).Scan(&maxTs); err != nil {
		t.Fatalf("query max(ts): %v", err)
	}
	got := r.UpdatedAt.AsTime()
	if got.Sub(maxTs) > time.Second || maxTs.Sub(got) > time.Second {
		t.Errorf("UpdatedAt = %v, want max(ts) = %v（反映存储 ts 而非 RPC now）", got, maxTs)
	}
}

// TestGetRepoRateRadar_StaleData 数据陈旧（~24h）→ UpdatedAt 反映真实陈旧时间。
// 旧 bug：updatedAt=now 始终报当前时间，掩盖陈旧数据。
func TestGetRepoRateRadar_StaleData(t *testing.T) {
	s := repoRadarTestStore(t)
	ctx := context.Background()
	// 插入 ~24h 前的数据
	staleTs := time.Now().UTC().Add(-24 * time.Hour)
	if err := s.InsertRepoRate(ctx, "204007", "GC007", 1.5, nil, staleTs); err != nil {
		t.Fatalf("InsertRepoRate: %v", err)
	}
	// 手动改 ts 为 24h 前（InsertRepoRate 的 ts 由 DB now() 生成）
	if _, err := s.Pool().Exec(ctx,
		`UPDATE fin_repo_rates SET ts = $1 WHERE code = '204007'`, staleTs); err != nil {
		t.Fatalf("UPDATE ts: %v", err)
	}
	qbus := bus.New()
	svc := &Service{store: s, qbus: qbus, freshTTL: time.Minute, liqMMR: 0.005}
	resp, err := svc.GetRepoRateRadar(ctx, connect.NewRequest(&v1.GetRepoRateRadarRequest{}))
	if err != nil {
		t.Fatalf("GetRepoRateRadar: %v", err)
	}
	if resp.Msg.UpdatedAt == nil {
		t.Fatal("UpdatedAt = nil, want non-nil")
	}
	age := time.Since(resp.Msg.UpdatedAt.AsTime())
	// 24h ± 1h（允许 DB 时钟微差）
	if age < 23*time.Hour || age > 25*time.Hour {
		t.Errorf("stale data: UpdatedAt age = %v, want 23-25h（反映真实陈旧时间，非 now）", age)
	}
}

// TestGetRepoRateRadar_MixedData 混合数据（1 行 24h 前 + 1 行当前）→ 最新行 ts 胜出。
func TestGetRepoRateRadar_MixedData(t *testing.T) {
	s := repoRadarTestStore(t)
	ctx := context.Background()
	// 1 行 24h 前
	staleTs := time.Now().UTC().Add(-24 * time.Hour)
	if err := s.InsertRepoRate(ctx, "204001", "GC001", 1.3, nil, staleTs); err != nil {
		t.Fatalf("InsertRepoRate stale: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`UPDATE fin_repo_rates SET ts = $1 WHERE code = '204001'`, staleTs); err != nil {
		t.Fatalf("UPDATE ts: %v", err)
	}
	// 1 行当前（GC007）
	if err := s.InsertRepoRate(ctx, "204007", "GC007", 1.4, nil, time.Now().UTC()); err != nil {
		t.Fatalf("InsertRepoRate current: %v", err)
	}
	qbus := bus.New()
	svc := &Service{store: s, qbus: qbus, freshTTL: time.Minute, liqMMR: 0.005}
	resp, err := svc.GetRepoRateRadar(ctx, connect.NewRequest(&v1.GetRepoRateRadarRequest{}))
	if err != nil {
		t.Fatalf("GetRepoRateRadar: %v", err)
	}
	if resp.Msg.UpdatedAt == nil {
		t.Fatal("UpdatedAt = nil, want non-nil")
	}
	// 最新行（GC007 当前）应胜出，age 应 < 1h（非 24h）
	age := time.Since(resp.Msg.UpdatedAt.AsTime())
	if age > time.Hour {
		t.Errorf("mixed data: UpdatedAt age = %v, want < 1h（当前行 ts 应胜出，非 24h 陈旧行）", age)
	}
}

// TestGetRepoRateRadar_Empty 空表 → 全零响应，不报错。
func TestGetRepoRateRadar_Empty(t *testing.T) {
	s := repoRadarTestStore(t)
	ctx := context.Background()
	qbus := bus.New()
	svc := &Service{store: s, qbus: qbus, freshTTL: time.Minute, liqMMR: 0.005}
	resp, err := svc.GetRepoRateRadar(ctx, connect.NewRequest(&v1.GetRepoRateRadarRequest{}))
	if err != nil {
		t.Fatalf("GetRepoRateRadar empty: %v", err)
	}
	r := resp.Msg
	// 空表 → rates 为空
	if len(r.Rates) != 0 {
		t.Errorf("empty table: Rates len = %d, want 0", len(r.Rates))
	}
	// OHLC 全零（COALESCE 兜底）
	if r.Gc007Open != 0 || r.Gc007Close != 0 || r.Gc007High != 0 || r.Gc007Low != 0 {
		t.Errorf("empty table OHLC should be 0, got open=%v close=%v high=%v low=%v",
			r.Gc007Open, r.Gc007Close, r.Gc007High, r.Gc007Low)
	}
	// 30d 均值 = 0
	if r.Gc007_30DAvg != 0 {
		t.Errorf("empty table: Gc007_30DAvg = %v, want 0", r.Gc007_30DAvg)
	}
	// R4-2：空表 → UpdatedAt == nil（不报当前时间）
	if r.UpdatedAt != nil {
		t.Errorf("empty table: UpdatedAt = %v, want nil（空表不报当前时间）", r.UpdatedAt.AsTime())
	}
}

// TestGetRepoRateRadar_HttpSmoke HTTP smoke：通过 ConnectRPC 客户端调用。
func TestGetRepoRateRadar_HttpSmoke(t *testing.T) {
	s := repoRadarTestStore(t)
	seedRepoRates(t, s)
	ctx := context.Background()
	qbus := bus.New()
	svc := &Service{store: s, qbus: qbus, freshTTL: time.Minute, liqMMR: 0.005}
	path, handler := dashboardv1connect.NewDashboardServiceHandler(svc)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := dashboardv1connect.NewDashboardServiceClient(http.DefaultClient, srv.URL)
	resp, err := client.GetRepoRateRadar(ctx, connect.NewRequest(&v1.GetRepoRateRadarRequest{}))
	if err != nil {
		t.Fatalf("HTTP GetRepoRateRadar: %v", err)
	}
	if len(resp.Msg.Rates) != 4 {
		t.Errorf("HTTP smoke: Rates len = %d, want 4", len(resp.Msg.Rates))
	}
}
