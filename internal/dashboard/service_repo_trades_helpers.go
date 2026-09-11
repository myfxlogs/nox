// D-272 逆回购操作记录辅助函数（从 service_repo_trades.go 拆出，保 ≤300 行）。
package dashboard

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"arb/internal/store/pgstore"
)

// instrumentNames 后端硬编码 code→name 映射（非 DB 列）。
var instrumentNames = map[string]string{
	"204001": "GC001",
	"204007": "GC007",
	"204014": "GC014",
	"204028": "GC028",
}

// validInstrumentCodes 支持的品种列表。
var validInstrumentCodes = map[string]bool{
	"204001": true,
	"204007": true,
	"204014": true,
	"204028": true,
}

// feeRates 逆回购佣金率（成交金额 × 费率 = 手续费，一次性非年化非按占用天数）。
// 204001=生产实证 9-11（407,000×0.00001=4.07）；其余三品种按上交所公布回购佣金率，待首个实测成交日后校准。
var feeRates = map[string]float64{
	"204001": 0.00001,
	"204007": 0.00005,
	"204014": 0.00010,
	"204028": 0.00020,
}

// computeDefaultMaturityDate §0 A-1 修订：从 trade_date+1 起有界前扫（≤15天）首个工作日。
// 扫描越界或日历查询出错 → 回退 trade_date+1。
func computeDefaultMaturityDate(ctx context.Context, store *pgstore.Store, tradeDate time.Time) time.Time {
	fallback := tradeDate.AddDate(0, 0, 1)
	for i := 1; i <= 15; i++ {
		candidate := tradeDate.AddDate(0, 0, i)
		biz, err := store.CNFICalendarIsBusinessDay(ctx, candidate)
		if err != nil {
			return fallback // 日历查询出错 → 回退
		}
		if biz {
			return candidate
		}
	}
	return fallback // 扫描越界 → 回退
}

// buildSignalSnapshot 生成 signal_snapshot JSON（设计 §3 钉死结构）。
func buildSignalSnapshot(code string, ratePct *float64, avg30d float64, avgHas bool, idleCny float64, theoreticalReturnByIdle *float64) []byte {
	type snapshot struct {
		InstrumentCode          string   `json:"instrument_code"`
		RatePct                 *float64 `json:"rate_pct"`
		Avg30d                  *float64 `json:"avg30d"`
		IdleCny                 float64  `json:"idle_cny"`
		TheoreticalReturnByIdle *float64 `json:"theoretical_return_by_idle"`
		Source                  string   `json:"source"`
	}
	snap := snapshot{
		InstrumentCode: code,
		RatePct:        ratePct,
		IdleCny:        idleCny,
	}
	if avgHas {
		snap.Avg30d = &avg30d
	}
	if ratePct != nil {
		snap.TheoreticalReturnByIdle = theoreticalReturnByIdle
		snap.Source = "fin_repo_rates"
	} else {
		snap.Source = "empty(非交易时段)"
	}
	b, _ := json.Marshal(snap)
	return b
}

// isUniqueViolation 判断是否唯一约束冲突（pgx 错误码 23505）。
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "23505")
}
