package pgstore

// D-327 S1：entry spread 基准计算与补写（XASSET_CARRY 开仓 meta 写入点与 boot 回填共用）。
// 独立成文保 positions.go ≤300 行门禁。

import "context"

// EntrySpreadBpsFromLegs D-327 S1：从 OPEN FILLED 双腿成交价算 entry spread 基准。
// 单腿缺失/price_actual nil/买价非正 → ok=false（fail-closed 不猜，不写）。
// 原 autotrade computeEntrySpreadFromLegs 实现下沉至此（P3 公式单一真相源）。
func (s *Store) EntrySpreadBpsFromLegs(ctx context.Context, posID int64) (float64, bool, error) {
	legs, err := s.ListPositionLegs(ctx, posID)
	if err != nil {
		return 0, false, err
	}
	bps, ok := EntrySpreadBpsFromLegSlice(legs)
	return bps, ok, nil
}

// EntrySpreadBpsFromLegSlice D-391：同口径作用于内存腿切片（调用方已持有 legs 时免 DB 重查）。
// 公式=(buyActual−sellActual)/buyActual×10000（成交价口径含滑点，跨 canonical 天然兼容）。
func EntrySpreadBpsFromLegSlice(legs []PositionLeg) (float64, bool) {
	var buyLeg, sellLeg *PositionLeg
	for i := range legs {
		if legs[i].Phase != "OPEN" {
			continue
		}
		if legs[i].Side == "BUY" {
			buyLeg = &legs[i]
		} else {
			sellLeg = &legs[i]
		}
	}
	if buyLeg == nil || sellLeg == nil {
		return 0, false
	}
	if buyLeg.PriceActual == nil || sellLeg.PriceActual == nil {
		return 0, false
	}
	buyActual := *buyLeg.PriceActual
	sellActual := *sellLeg.PriceActual
	if buyActual <= 0 {
		return 0, false
	}
	return (buyActual - sellActual) / buyActual * 10000, true
}

// EnsureEntrySpreadFromLegs D-327 S1：计算并补写 entry spread 基准（XASSET_CARRY 开仓
// meta 写入点与 boot 回填共用）。UpdateExpectedEntrySpreadBps 自带 IS NULL 幂等（已有基准
// 不覆写）；ok=false（腿未 FILLED/缺成交价）静默返回 nil，留给下一写点或回填补齐。
func (s *Store) EnsureEntrySpreadFromLegs(ctx context.Context, posID int64) error {
	bps, ok, err := s.EntrySpreadBpsFromLegs(ctx, posID)
	if err != nil || !ok {
		return err
	}
	return s.UpdateExpectedEntrySpreadBps(ctx, posID, bps)
}
