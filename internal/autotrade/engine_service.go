package autotrade

import (
	"context"

	"arb/internal/bus"
	"arb/internal/store/pgstore"
)

// engineService D-508 S7b：autotrade 对 dashboard.Service 的窄接口解绑——
// 引擎只依赖被调方法集（dashboard.Service 结构满足，cmd/core 注入不变），
// autotrade 包不再 import dashboard（逆向依赖切除）。
type engineService interface {
	// 资金注入提醒（D-072）
	SwapArbFundingNeedUSD(canonical, venueLong, venueShort string) (needUSD float64, ok bool)
	PublishFundingNeed(bus.FundingNeedNotice)
	// sizing/门槛
	USDPerLotForLeg(canonical, venueKey, side string) (float64, bool)
	QtyBelowMinStep(venueKey, venueSymbol string, qty float64) bool
	SwapArbNotional(canonical, venueLong, venueShort string) (notional float64, ok bool, reason string)
	SwapArbVenueExposureOK(ctx context.Context, venueLong, venueShort string, newNotional float64) bool
	MarginFloorKnown(ctx context.Context, venueKey string, addNotionalUSD float64) (ok, known bool, detail string)
	MarginFloorCheck(ctx context.Context, venueKey string, addNotionalUSD float64) (ok bool, detail string) // Gate 开户门槛档
	VenueCommissionBps(venueKey string) float64
	AccountLeverage(venueKey string) (leverage float64, ok bool)
	AccountEquityUSD(venueKey string) (value float64, currency string, ok bool)
	// 执行/平仓
	ExecuteOpportunityInternal(ctx context.Context, oppID, requestID string, qty float64) (status, message string, positionID int64)
	ExecuteMeanRevert(ctx context.Context, canonical, venueBuy, venueSell, requestID string, qty, openBps float64) (status, message string, positionID int64)
	ExecuteMeanRevertWithCap(ctx context.Context, canonical, venueBuy, venueSell, requestID string, qty, openBps, maxNotional float64, strategyType string) (status, message string, positionID int64)
	AutoClosePosition(ctx context.Context, posID int64) error
	// 解析器/存储透传
	LegContractSize() func(venueKey, venueSymbol string) (float64, bool)
	Store() *pgstore.Store
}
