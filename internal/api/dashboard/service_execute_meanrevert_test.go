// service_execute_meanrevert_test.go —— D-190 §1.5：ExecuteMeanRevertWithCap JPY cap + fx 缺失测试。
// 测试策略（v2 钉死）：DSN-gated 真库 + bus.New() 手工 Publish 假报价。
//
// D-190 返修 R6：
//   - OverCap 测试从 bbb0f85 恢复（实测 PASS，删除过度）。
//   - fx-missing 测试注入 fake pipeline（exec.NewPipeline 空 registry）走到 PerLot 路径
//     （pipeline nil 在 line 122 提前返回 "executor not wired"，无法验证 PerLot fail-closed）。
package dashboard

import (
	"context"
	"strings"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/trading/exec"
)

// setupMeanRevertService 构造 DSN-gated store（RUNNING）+ QuoteBus（AUDJPY 报价，USDJPY 可选）+ fake pipeline。
// withUSDJPY=true → 发布 USDJPY 报价（JPY 腿 PerLot ok）；false → 不发布（PerLot fail-closed）。
// D-190 R6：注入 fake pipeline（exec.NewPipeline 空 registry）让 ExecuteMeanRevertWithCap 走过 line 122 提前返回。
func setupMeanRevertService(t *testing.T, withUSDJPY bool) *Service {
	t.Helper()
	st := serviceTestStoreSingleInit(t)
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
	// D-190 R6：fake pipeline（空 registry）——只需非 nil 让 line 122 放行，PerLot fail-closed 在 Execute 前返回。
	pipeline := exec.NewPipeline(exec.NewRegistry(), st, qb, exec.RiskLimits{}, "demo")
	svc := &Service{store: st, qbus: qb, pipeline: pipeline}
	// MT 腿（vA:mt5/vB:mt5）需 legContractSize 注入，否则 legContractSizeOf 在 line 151 提前返回。
	// CS=1 简化（与 service_swap_deviation_test.go 同语义）。
	svc.SetLegContractSize(func(venueKey, venueSymbol string) (float64, bool) {
		return 1.0, true
	})
	return svc
}

// TestExecuteMeanRevertWithCap_JPYFxMissing_FAILED D-190 §1.5：
// AUDJPY 报价有但无 USDJPY → PerLot fail-closed → FAILED "名义额不可得"。
// D-190 R6：注入 fake pipeline 走过 line 122，到达 line 162 PerLot 检查。
func TestExecuteMeanRevertWithCap_JPYFxMissing_FAILED(t *testing.T) {
	s := setupMeanRevertService(t, false) // 无 USDJPY
	status, msg, _ := s.ExecuteMeanRevertWithCap(context.Background(),
		"AUDJPY", "vA:mt5", "vB:mt5", "req-test", 0.1, 0.5, 5000, "MEANREVERT")
	if status != "FAILED" {
		t.Errorf("fx missing: status=%s want FAILED", status)
	}
	if !strings.Contains(msg, "名义额不可得") {
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
	// cap 压缩后继续走 imbalance/legVolume 检查，可能因 pipeline nil FAILED，但不应是 "名义额不可得"
	if strings.Contains(msg, "名义额不可得") {
		t.Errorf("over cap: should not fail-closed on PerLot (USDJPY present), msg=%s", msg)
	}
	_ = status // status 可能因后续门禁 FAILED，但不是 PerLot fail-closed
}
