// paper_store.go —— D-350 阶段②：纸面轨存储依赖接口（自 paper_spot_carry.go
// 拆出，文件行数闸；D-051② 扩入 PaperCexGateStore 子接口）。*pgstore.Store 原生实现。
package scan

import (
	"context"
	"time"

	"arb/internal/store/pgstore"
)

// PaperStore D-350 阶段②：纸面轨存储依赖（*pgstore.Store 原生实现）。
type PaperStore interface {
	ListCexUniverse(ctx context.Context) (map[string]pgstore.CexUniverseRow, error)
	ListActivePaperSpotCarry(ctx context.Context) ([]pgstore.PaperSpotCarry, error)
	ListAccrablePaperSpotCarry(ctx context.Context) ([]pgstore.PaperSpotCarry, error)
	InsertPaperSpotCarry(ctx context.Context, r *pgstore.PaperSpotCarry) error
	AccruePaperSpotCarry(ctx context.Context, id int64, lastFundingAnn, accruedAdd float64, lowStreak, missStreak int, lastBasisBps *float64, accrueTo *time.Time, accruedReplace bool) error
	// demo 行实收读面+口径注记（*pgstore.Store 原生实现）。
	SumIncomeWindow(ctx context.Context, venue, symbol string, from, to time.Time) (float64, int, error)
	AnnotatePaperNote(ctx context.Context, id int64, marker string) error
	// 结算对齐累积 facts 读面（*pgstore.Store 原生实现）。
	PaperFactSource
	// 退出冻结收敛捕获（nil→列 NULL 诚实缺）。
	ExitPaperSpotCarry(ctx context.Context, id int64, reason string, convergenceUSD *float64) error
	// cfd 轨 monitor basis 面与保底槽数据源（*pgstore.Store 原生实现）。
	XAssetMonitorRowNear(ctx context.Context, pair, cexVenue, mtVenue string, ts time.Time, maxGap time.Duration) (entrySpreadBps float64, ok bool, err error)
	LatestXAssetEntryGate(ctx context.Context, pair, cexVenue, mtVenue string, maxAge time.Duration) (entryBps, noiseBps, harvestFloorBps float64, hasFloor, ok bool, err error)
	// 授权组合 monitor 簿存在性哨兵（纯存在性无 maxAge）。
	HasXAssetMonitorRow(ctx context.Context, pair, cexVenue, mtVenue string) (bool, error)
	LoadXAssetAllowCombos(ctx context.Context, environment string) ([]pgstore.XAssetAllowRow, error)
	LatestPaperExitTs(ctx context.Context, venueA, venueB, symbol string) (*time.Time, error)
	// data_lost 冷却计数（24h 窗内 ≥2 次 → 冷却不开）。
	CountDataLostExits(ctx context.Context, venueA, venueB, symbol string, since time.Time) (int, error)
	// 保底槽宇宙行缺席 warn 节流（禁 `_ =` 吞错，cashflow-write-error 同族条款）。
	InsertAlert(ctx context.Context, ruleID *int64, level, message string) error
	PaperCexGateStore // D-051② 纸面入口质量闸读面（子接口，paper_cex_gate_store.go）
	// mt×mt 轨 entry_basis/费用 tick 读面（*pgstore.Store 原生实现）。
	TicksWindow(ctx context.Context, venue, symbol string, since time.Time) ([]pgstore.TickRow, error)
	// 费率源 exchange_configs.taker_fee_bps（*pgstore.Store 原生实现；缺→拒填）。
	ExchangeTakerFeeBps(ctx context.Context, venue, venueType string) (float64, bool, error)
	// ListAssetEquiv D-043：等价类映射（class 键行 legs/pair 解析单源；*pgstore.Store 原生实现）。
	ListAssetEquiv(ctx context.Context) ([]pgstore.AssetEquivEntry, error)
}
