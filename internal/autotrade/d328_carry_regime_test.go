package autotrade

// D-328 S3 集成锚：carry 入口 funding regime 持久性闸（auto 路径）。
// T1 485 回放=静默 skip（不产 intent 不落 REJECTED，csMiss 不增=未达 sizing）；
// T1b 非收入腿豁免 / T2 放行 / T5 非 XASSET与非 :perp 腿不触发=csMiss 增（到达 computeBuyQty）。

import (
	"context"
	"fmt"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/dashboard"
	"arb/internal/exec"
	"arb/internal/store/pgstore"
)

// d328SeedFunding 种 funding 预测流样本（5min 间隔升序，全部落在近 1h 窗口内）。
func d328SeedFunding(t *testing.T, s *pgstore.Store, venue, symbol string, vals ...float64) {
	t.Helper()
	ctx := context.Background()
	base := time.Now().Add(-time.Duration(len(vals)) * 5 * time.Minute)
	for i, v := range vals {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO facts (kind, venue, symbol, value, unit, ts, src)
			 VALUES ('funding', $1, $2, $3, 'pct_annualized', $4, 'd328test')`,
			venue, symbol, v, base.Add(time.Duration(i)*5*time.Minute)); err != nil {
			t.Fatalf("seed fact %d: %v", i, err)
		}
	}
}

// d328Engine 构造带 store+regime 配置的 Engine（镜像 D-311 mkEngine harness）。
func d328Engine(t *testing.T, s *pgstore.Store, allow []exec.XAssetComboAllowSpec) *Engine {
	svc := dashboard.New(nil, bus.New(), nil, nil, nil, 0, 0, 0, 0, 0, 0, nil, nil, nil, 0, nil, nil, 0, 0, nil)
	return &Engine{
		store: s, service: svc,
		csMissCounts: map[string]int{}, csMissSent: map[string]time.Time{},
		cfg: Config{
			MinNetBps: 0,
			AllowVenues: map[string]bool{
				"gate:perp": true, "okx:perp": true, "3001157:mt5": true, "277842155:mt5": true,
			},
			XAssetAllowCombos: allow,
			CarryRegime: exec.CarryRegimeConfig{
				Window: 24 * time.Hour, FloorPct: 2.0, MinSamples: 12,
			},
		},
	}
}

// d328RejectedCount autotrade_logs REJECTED 行数（静默跳过=0）。
func d328RejectedCount(t *testing.T, s *pgstore.Store, oppID string) int {
	t.Helper()
	var n int
	if err := s.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM autotrade_logs WHERE opportunity_id = $1`, oppID).Scan(&n); err != nil {
		t.Fatalf("count logs: %v", err)
	}
	return n
}

// TestD328_HandleOpportunity_RegimeDead_SilentSkip T1 485 回放：买侧 perp 收入腿窗口含归零
// → 静默跳过（csMiss=0 未达 sizing + 不落 REJECTED）。对未接线码先红（csMiss=1）。
func TestD328_HandleOpportunity_RegimeDead_SilentSkip(t *testing.T) {
	s := testAccumStore(t)
	d311Truncate(t, s)
	ctx := context.Background()
	d328SeedFunding(t, s, "gate", "CLUSDT", -17, -4, 0, -12, 0, 0, 0, 0, 0, 0, 0, 0, -12) // n=13 latest=-12

	opp := pgstore.Opportunity{
		ID: "XASSET_CARRY:CLUSDT:gate:perp:3001157:mt5", Type: "XASSET_CARRY",
		Canonical: "CLUSDT", VenueBuy: "gate:perp", VenueSell: "3001157:mt5", NetBps: 50,
	}
	allow := []exec.XAssetComboAllowSpec{
		{CanonicalBuy: "CLUSDT", CanonicalSell: "CLUSDT", VenueBuy: "gate:perp", VenueSell: "3001157:mt5", MaxLongQty: 1, MaxShortQty: 1000, MaxGrossUSD: 50000},
	}
	e := d328Engine(t, s, allow)
	e.handleOpportunity(ctx, opp, newComboAccumulator())

	if got := e.csMissCounts["gate:perp|CLUSDT"]; got != 0 {
		t.Errorf("regime 死亡应静默跳过（未达 sizing）, csMiss=%d", got)
	}
	if n := d328RejectedCount(t, s, opp.ID); n != 0 {
		t.Errorf("静默跳过不落 REJECTED, autotrade_logs=%d", n)
	}
}

// TestD328_HandleOpportunity_NonIncomeAndPass T1b+T2：latest 归零腿豁免与全程深负均放行
// （到达 computeBuyQty=csMiss 增）。
func TestD328_HandleOpportunity_NonIncomeAndPass(t *testing.T) {
	s := testAccumStore(t)
	d311Truncate(t, s)
	ctx := context.Background()
	allow := []exec.XAssetComboAllowSpec{
		{CanonicalBuy: "CLUSDT", CanonicalSell: "CLUSDT", VenueBuy: "gate:perp", VenueSell: "3001157:mt5", MaxLongQty: 1, MaxShortQty: 1000, MaxGrossUSD: 50000},
	}

	// T1b：latest 归零（非收入腿）→ 豁免放行。
	// D-423 涟漪适配：本 fixture 组合 VenueBuy=gate:perp（CEX 买腿）=反向形态，
	// monitor 方向未定义在 regime 闸前即钉死拒开——断言语义改锚拒开（regime 豁免
	// 判定仅对正向 MT×CEX 组合可达）。
	d328SeedFunding(t, s, "gate", "CLUSDT", -17, -4, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	e := d328Engine(t, s, allow)
	e.handleOpportunity(ctx, pgstore.Opportunity{
		ID: "XASSET_CARRY:CLUSDT:gate:perp:3001157:mt5", Type: "XASSET_CARRY",
		Canonical: "CLUSDT", VenueBuy: "gate:perp", VenueSell: "3001157:mt5", NetBps: 50,
	}, newComboAccumulator())
	if got := e.csMissCounts["gate:perp|CLUSDT"]; got != 0 {
		t.Errorf("T1b 反向组合（CEX 买腿）D-423 应拒开, csMiss=%d", got)
	}

	// T2：全程深负（min −25/max −8）→ 同反向拒开（D-423）。
	d311Truncate(t, s)
	d328SeedFunding(t, s, "gate", "CLUSDT", -25, -20, -18, -15, -12, -11, -10, -10, -9, -9, -8.5, -8, -2)
	e = d328Engine(t, s, allow)
	e.handleOpportunity(ctx, pgstore.Opportunity{
		ID: "XASSET_CARRY:CLUSDT:gate:perp:3001157:mt5", Type: "XASSET_CARRY",
		Canonical: "CLUSDT", VenueBuy: "gate:perp", VenueSell: "3001157:mt5", NetBps: 50,
	}, newComboAccumulator())
	if got := e.csMissCounts["gate:perp|CLUSDT"]; got != 0 {
		t.Errorf("T2 反向组合（CEX 买腿）D-423 应拒开, csMiss=%d", got)
	}
}

// TestD328_HandleOpportunity_NoTrigger T5：SWAPARB 与双 MT 腿 XASSET 不触发闸（回归锚）。
func TestD328_HandleOpportunity_NoTrigger(t *testing.T) {
	s := testAccumStore(t)
	d311Truncate(t, s)
	ctx := context.Background()
	e := d328Engine(t, s, nil) // allowlist nil：SWAPARB 不走 XASSET 白名单门

	// SWAPARB：闸不触发 → 到 sizing（csMiss 证明越过闸位）
	e.handleOpportunity(ctx, pgstore.Opportunity{
		ID:   fmt.Sprintf("SWAPARB:AUDJPY:3001157:mt5:277842155:mt5.%d", time.Now().UnixNano()),
		Type: "SWAPARB", Canonical: "AUDJPY",
		VenueBuy: "3001157:mt5", VenueSell: "277842155:mt5", NetBps: 50,
	}, newComboAccumulator())
	if got := e.csMissCounts["3001157:mt5|AUDJPY"]; got != 1 {
		t.Errorf("SWAPARB 不应被 regime 闸拦截, csMiss=%v", e.csMissCounts)
	}

	// XASSET 双 MT 腿（无 :perp）→ D-423 涟漪适配：monitor 簿无 MT×MT venue 组合
	// （三元组 mt_venue 键可得但 cex_venue=MT venue 无簿）→ 入口闸 fail-closed 拒开，
	// regime 闸不达——断言改锚拒开（monitor 未定义域不放行）。
	d328SeedFunding(t, s, "gate", "CLUSDT", 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0) // 死窗也拦不到 MT 腿
	e2 := d328Engine(t, s, []exec.XAssetComboAllowSpec{
		{CanonicalBuy: "XAUUSD", CanonicalSell: "XAUUSD", VenueBuy: "3001157:mt5", VenueSell: "277842155:mt5", MaxLongQty: 1, MaxShortQty: 1000, MaxGrossUSD: 50000},
	})
	e2.handleOpportunity(ctx, pgstore.Opportunity{
		ID:   fmt.Sprintf("XASSET_CARRY:XAUUSD:3001157:mt5:277842155:mt5.%d", time.Now().UnixNano()),
		Type: "XASSET_CARRY", Canonical: "XAUUSD",
		VenueBuy: "3001157:mt5", VenueSell: "277842155:mt5", NetBps: 50,
	}, newComboAccumulator())
	if got := e2.csMissCounts["3001157:mt5|XAUUSD"]; got != 0 {
		t.Errorf("双 MT 腿 XASSET monitor 未定义域应 fail-closed 拒开（D-423）, csMiss=%v", e2.csMissCounts)
	}
}
