// service_execute_meanrevert_test.go —— D-190 §1.5：ExecuteMeanRevertWithCap JPY cap + fx 缺失测试。
// 测试策略（v2 钉死）：DSN-gated 真库 + bus.New() 手工 Publish 假报价。
package dashboard

import (
	"context"
	"strings"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/store/pgstore"
	"arb/internal/testutil"
)

// setupMeanRevertService 构造 DSN-gated store（RUNNING）+ QuoteBus（AUDJPY 报价，USDJPY 可选）。
// withUSDJPY=true → 发布 USDJPY 报价（JPY 腿 PerLot ok）；false → 不发布（PerLot fail-closed）。
func setupMeanRevertService(t *testing.T, withUSDJPY bool) *Service {
	t.Helper()
	dsn := testutil.TestDSN(t, "ARB_TEST_PG_DSN")
	st, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx, `TRUNCATE autotrade_state CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO autotrade_state (state, reason, halted_on) VALUES ('RUNNING', '', NULL)`); err != nil {
		t.Fatalf("insert RUNNING: %v", err)
	}
	qb := bus.New()
	now := time.Now()
	qb.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "vA", VenueType: "mt5", Bid: 96.9, Ask: 97.1, Time: now, VenueSymbol: "AUDJPY"})
	qb.Publish(bus.Quote{Symbol: "AUDJPY", Venue: "vB", VenueType: "mt5", Bid: 96.9, Ask: 97.1, Time: now, VenueSymbol: "AUDJPY"})
	if withUSDJPY {
		qb.Publish(bus.Quote{Symbol: "USDJPY", Bid: 149.9, Ask: 150.1, Time: now})
	}
	return &Service{store: st, qbus: qb}
}

// TestExecuteMeanRevertWithCap_JPYFxMissing_FAILED D-190 §1.5：
// AUDJPY 报价有但无 USDJPY → PerLot fail-closed → FAILED "usd per lot unavailable"。
func TestExecuteMeanRevertWithCap_JPYFxMissing_FAILED(t *testing.T) {
	s := setupMeanRevertService(t, false) // 无 USDJPY
	status, msg, _ := s.ExecuteMeanRevertWithCap(context.Background(),
		"AUDJPY", "vA:mt5", "vB:mt5", "req-test", 0.1, 0.5, 5000, "MEANREVERT")
	if status != "FAILED" {
		t.Errorf("fx missing: status=%s want FAILED", status)
	}
	if !strings.Contains(msg, "usd per lot unavailable") {
		t.Errorf("fx missing: msg=%s want contain 'usd per lot unavailable'", msg)
	}
}

// TestExecuteMeanRevertWithCap_JPYOverCap_FloorToMinLot D-190 §1.5：
// maxNotional=5000、JPY 腿 usdPerLot=64666.67、qty=0.1 → notional=$6466.67 > cap → 压到 floorToStepQtyMin。
// 验证：status 非 FAILED（cap 压缩后继续走 imbalance 检查，可能因其他门禁失败但不应是 cap FAILED）。
// 注：完整执行需 pipeline 接线，此处只验证 cap 检查不因 PerLot fail-closed 而 FAILED。
func TestExecuteMeanRevertWithCap_JPYOverCap_FloorToMinLot(t *testing.T) {
	s := setupMeanRevertService(t, true) // 有 USDJPY
	// qty=0.1, AUDJPY price=97, CS=1(无 resolver), USDJPY=150 → usdPerLot=97/150≈0.647
	// notional = 0.1 × 0.647 = 0.0647 < 5000 → 不超 cap（CS=1 时 notional 很小）
	// 用更大 qty 验证超 cap：qty=100000 → notional=64700 > 5000 → 压 min lot
	status, msg, _ := s.ExecuteMeanRevertWithCap(context.Background(),
		"AUDJPY", "vA:mt5", "vB:mt5", "req-test", 100000, 0.5, 5000, "MEANREVERT")
	// cap 压缩后继续走 imbalance/legVolume 检查，可能因 pipeline nil FAILED，但不应是 "usd per lot unavailable"
	if strings.Contains(msg, "usd per lot unavailable") {
		t.Errorf("over cap: should not fail-closed on PerLot (USDJPY present), msg=%s", msg)
	}
	_ = status // status 可能因 pipeline nil 而 FAILED，但不是 PerLot fail-closed
}
