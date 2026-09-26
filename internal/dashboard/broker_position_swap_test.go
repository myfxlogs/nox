package dashboard

// D-152 微件①单测：CEX 腿 swap 聚合（annotateCEXSwapWithMap 纯函数）+ MT 透传回归。
// annotateCEXSwap（带 DB）由集成测试覆盖；此处测纯函数 annotateCEXSwapWithMap。

import (
	"math"
	"testing"

	v1 "arb/internal/gen/dashboardv1"
)

// TestAnnotateCEXSwapWithMap_CEXAggregated CEX 仓 position_id>0 + cashflow map 有值 → swap 被覆盖。
// binance:perp pos=456 有 cashflow sum=3.5 → swap=3.5（替换原 0）。
func TestAnnotateCEXSwapWithMap_CEXAggregated(t *testing.T) {
	bps := []*v1.BrokerPosition{
		{VenueKey: "binance:perp", Venue: "binance", Ticket: "BTCUSDT", PositionId: 456, Swap: 0},
		{VenueKey: "okx:perp", Venue: "okx", Ticket: "ETH-USDT-SWAP", PositionId: 456, Swap: 0},
	}
	sums := map[string]float64{
		"456\x00binance:perp": 3.5,
		"456\x00okx:perp":     1.2,
	}
	annotateCEXSwapWithMap(bps, sums)
	if math.Abs(bps[0].Swap-3.5) > 0.001 {
		t.Errorf("binance:perp swap = %.4f, want 3.5 (cashflow aggregated)", bps[0].Swap)
	}
	if math.Abs(bps[1].Swap-1.2) > 0.001 {
		t.Errorf("okx:perp swap = %.4f, want 1.2 (cashflow aggregated)", bps[1].Swap)
	}
}

// TestAnnotateCEXSwapWithMap_NoCashflowKeepsOriginal CEX 仓 position_id>0 但 cashflow map 无值 → 保留原 swap。
// Gate funding=0.0023 从 API 透传，无 cashflow 行 → 保留 0.0023。
func TestAnnotateCEXSwapWithMap_NoCashflowKeepsOriginal(t *testing.T) {
	bps := []*v1.BrokerPosition{
		{VenueKey: "gate:perp", Venue: "gate", Ticket: "BTC_USDT", PositionId: 789, Swap: 0.0023},
	}
	sums := map[string]float64{} // 无该仓的 cashflow
	annotateCEXSwapWithMap(bps, sums)
	if math.Abs(bps[0].Swap-0.0023) > 0.0001 {
		t.Errorf("gate:perp swap = %.6f, want 0.0023 (保留原值，无 cashflow)", bps[0].Swap)
	}
}

// TestAnnotateCEXSwapWithMap_VenueTruthWins D-322 S3a：venue 真值优先——
// gate pnl_fund 已报实收（bp.Swap=+0.1209）不被 cashflow 估算（−0.1964）覆写；
// 旧码（D-152）无条件覆写，venue 真值被账本估算盖掉（pos 485 swap 列显示 −0.2 根因）。
func TestAnnotateCEXSwapWithMap_VenueTruthWins(t *testing.T) {
	bps := []*v1.BrokerPosition{
		{VenueKey: "gate:perp", Venue: "gate", Ticket: "CL_USDT", PositionId: 485, Swap: 0.1209},
	}
	sums := map[string]float64{
		"485\x00gate:perp": -0.1964, // 账本估算（错号行形态）
	}
	annotateCEXSwapWithMap(bps, sums)
	if math.Abs(bps[0].Swap-0.1209) > 0.0001 {
		t.Errorf("gate:perp swap = %.4f, want 0.1209 (venue 真值优先，不被估算覆写)", bps[0].Swap)
	}
}

// TestAnnotateCEXSwapWithMap_MTNotTouched MT 仓 swap 不动（Order.Swap 透传链完整）。
// 52993526:mt5 pos=456 swap=4.99 → 不被 cashflow 覆盖（isCEXVenueKey=false）。
func TestAnnotateCEXSwapWithMap_MTNotTouched(t *testing.T) {
	bps := []*v1.BrokerPosition{
		{VenueKey: "52993526:mt5", Venue: "52993526", Ticket: "12345", PositionId: 456, Swap: 4.99},
	}
	sums := map[string]float64{
		"456\x0052993526:mt5": 999.0, // 即使 map 有值，MT 仓也不动
	}
	annotateCEXSwapWithMap(bps, sums)
	if math.Abs(bps[0].Swap-4.99) > 0.001 {
		t.Errorf("MT swap = %.4f, want 4.99 (Order.Swap 透传，不被 cashflow 覆盖)", bps[0].Swap)
	}
}

// TestAnnotateCEXSwapWithMap_OrphanNotTouched position_id=0（孤儿订单）→ 不查 cashflow，swap 不动。
func TestAnnotateCEXSwapWithMap_OrphanNotTouched(t *testing.T) {
	bps := []*v1.BrokerPosition{
		{VenueKey: "binance:perp", Venue: "binance", Ticket: "BTCUSDT", PositionId: 0, Swap: 0},
	}
	sums := map[string]float64{
		"0\x00binance:perp": 999.0, // 不应被使用
	}
	annotateCEXSwapWithMap(bps, sums)
	if bps[0].Swap != 0 {
		t.Errorf("orphan swap = %.4f, want 0 (position_id=0 不查 cashflow)", bps[0].Swap)
	}
}

// TestAnnotateCEXSwapWithMap_MixedVenueSamePos 同一 position_id 不同 venue → 各自独立聚合。
// PERP_FUNDING pos=321 有 binance:spot（funding=2.0）+ okx:perp（funding=1.5）→ 各行 swap 独立。
func TestAnnotateCEXSwapWithMap_MixedVenueSamePos(t *testing.T) {
	bps := []*v1.BrokerPosition{
		{VenueKey: "binance:spot", Venue: "binance", Ticket: "BTCUSDT", PositionId: 321, Swap: 0},
		{VenueKey: "okx:perp", Venue: "okx", Ticket: "BTC-USDT-SWAP", PositionId: 321, Swap: 0},
	}
	sums := map[string]float64{
		"321\x00binance:spot": 2.0,
		"321\x00okx:perp":     1.5,
	}
	annotateCEXSwapWithMap(bps, sums)
	if math.Abs(bps[0].Swap-2.0) > 0.001 {
		t.Errorf("binance:spot swap = %.4f, want 2.0", bps[0].Swap)
	}
	if math.Abs(bps[1].Swap-1.5) > 0.001 {
		t.Errorf("okx:perp swap = %.4f, want 1.5", bps[1].Swap)
	}
}
