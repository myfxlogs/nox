// service_graduation_store.go —— 毕业授权存储依赖窄接口
// （自 service_carry_graduation.go 拆出控行数 <300；*pgstore.Store 原生满足，测试 fake 替身）。
package dashboard

import (
	"context"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// graduationStore approve/list 的存储依赖窄接口（*pgstore.Store 原生满足；测试 fake 替身）。
type graduationStore interface {
	GetCarryGraduation(ctx context.Context, id int64) (*pgstore.CarryGraduation, error)
	GetPaperSpotCarryByID(ctx context.Context, id int64) (*pgstore.PaperSpotCarry, error)
	ListSymbolMappings(ctx context.Context, venue string) ([]pgstore.SymbolMapping, error)
	ListBrokerAccounts(ctx context.Context) ([]pgstore.BrokerAccount, error)
	InsertXAssetAllowCombo(ctx context.Context, r *pgstore.XAssetComboInsert) (int64, error)
	UpdateCarryGraduationDecision(ctx context.Context, id int64, status string, comboID *int64, note string) error
	LoadXAssetAllowCombos(ctx context.Context, environment string) ([]pgstore.XAssetAllowRow, error)
	InsertAlert(ctx context.Context, ruleID *int64, level, message string) error
	ListCarryGraduations(ctx context.Context) ([]pgstore.CarryGraduation, error)
	// D-463 S4：reject 回写旗标投影表。
	InsertFlag(ctx context.Context, f *pgstore.VenueSymbolFlag) error
	// D-475a S3：approve 双闸——flag 活读（分叉前）+敞口 caps 活读（cfd 分支内）。
	ListActiveFlags(ctx context.Context) (map[string]pgstore.VenueSymbolFlag, error)
	ListVenueExposureCaps(ctx context.Context) (map[string]pgstore.VenueExposureCap, error)
	// D-475a S4：吊销编排存储面（裸 venue 段逐段停用+在管仓知情）。
	DisableCombosByFlag(ctx context.Context, bareVenue, symbol string) (int64, error)
	CountOpenPositionsOnVenue(ctx context.Context, bareVenue string) (int64, []int64, error)
	// D-475a S5：funding_interval 活读（观测 expected_periods 用）。
	LatestFacts(ctx context.Context, kind, venue, symbol string) ([]fact.Fact, error)
	// D-471 S1/S2：风险档案+入场复检读取面（ticks 5min 闸/universe/保证金率/funding 窗口/monitor p95）。
	LatestTick(ctx context.Context, venue, venueType, symbol string) (pgstore.TickRow, bool, error)
	ListCexUniverse(ctx context.Context) (map[string]pgstore.CexUniverseRow, error)
	FundingPredWindow(ctx context.Context, venue, symbol string, since time.Time) ([]pgstore.FactPoint, error)
	XAssetMonitorSpreadP95(ctx context.Context, cexVenue, mtVenue string) (float64, bool, error)
	// 3d-P1#1c：账户实时态 cfd real 键集（liveAccountLatest 写侧同源过滤面）。
	BrokerRealAccountKeys(ctx context.Context) (map[string]bool, error)
	// D-542：实拉校准配置源（livePoolQuerier 经代理链建 CEXAccountQuerier）。
	ListExchangeConfigs(ctx context.Context) ([]pgstore.ExchangeConfig, error)
	// D-557 S1：venue 行 join 通道白名单（allow_rule_id/enabled 透出）。
	ListAutotradeAllowRulesAll(ctx context.Context) ([]pgstore.AutotradeAllowRule, error)
	// D-471 S3：白名单管理面（全量行/行锁更新/在持仓查询）。
	ListXAssetAllowCombosAll(ctx context.Context) ([]pgstore.XAssetComboAdminRow, error)
	UpdateXAssetAllowCombo(ctx context.Context, r *pgstore.XAssetComboAdminRow) (bool, error)
	ListOpenPositionsForCombo(ctx context.Context, canonicalBuy, canonicalSell, venueBuy, venueSell string) ([]int64, float64, error)
	// D-560 S2b：近窗 FAILED autotrade_logs 批量面（拒事实透出，禁 N+1）。
	ListRecentFailedAutotradeLogs(ctx context.Context, window time.Duration) ([]pgstore.AutotradeLog, error)
	// D-544：非 mt×cex 结构 ticks 簿锚（ASOF 双腿对齐 adverse 成本 p95）。
	TickAdverseBasisP95(ctx context.Context, buyVenueKey, buySym, sellVenueKey, sellSym string, since time.Time) (float64, bool, error)
}

// gradDB 存储取用：测试可注入 gradStore 替身，生产回落 s.store（单一真源）。
func (s *Service) gradDB() graduationStore {
	if s.gradStore != nil {
		return s.gradStore
	}
	return s.store
}
