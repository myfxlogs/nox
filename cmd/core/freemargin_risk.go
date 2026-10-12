// freemargin_risk.go —— D-006 S2：MT5 FreeMargin 双因子告警（AccountSink 写点挂判）。
// 判据（SSOT 决策表）：warn = freeMargin < max(0.2×equity, 1.5×Σnotional/20)；
// critical = freeMargin ≤ Σnotional/20（收紧情景=按悲观档 1:20 全仓重估的保证金缺口）。
// MarginLevel% 在无限杠杆下分母失真，只作副证不进判据。
// 走 InsertAlert 既有 30min 同键去重通道；恢复不报（不再发=恢复）。纯观测闸：
// Σnotional 查询失败/告警写失败只 log，不告警不阻断。
package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"time"

	"nox/internal/data/keydomain"
	"nox/internal/store/pgstore"
)

// accountSinkWithFreeMargin D-006 S2：包装 acctMon.ObserveStreamAccount——
// 先原路落快照，后挂 FreeMargin 双因子判（事件驱动禁定时器）。
func accountSinkWithFreeMargin(ctx context.Context, st *pgstore.Store, observe func(vk string, bal, eq, mg, fm, prof, ml float64, ccy string, ts time.Time)) func(string, float64, float64, float64, float64, float64, float64, string, time.Time) {
	return func(vk string, bal, eq, mg, fm, prof, ml float64, ccy string, ts time.Time) {
		observe(vk, bal, eq, mg, fm, prof, ml, ccy, ts)
		checkMTFreeMarginRisk(ctx, st, vk, fm, eq)
	}
}

func checkMTFreeMarginRisk(ctx context.Context, st *pgstore.Store, venueKey string, freeMargin, equity float64) {
	if st == nil || !keydomain.IsType(venueKey, "mt5") {
		return
	}
	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	sumNotional, err := st.SumOpenNotionalUSDByVenue(qctx, keydomain.BareOf(venueKey))
	if err != nil {
		log.Printf("[freemargin] %s sum open notional: %v", venueKey, err)
		return
	}
	need := sumNotional / pessimisticLeverageD006
	level := freeMarginRiskLevel(freeMargin, equity, need)
	if level == "critical" {
		aerr := st.InsertAlert(qctx, nil, "critical",
			fmt.Sprintf("MT5 保证金缺口（收紧情景）：账户 %s freeMargin $%.0f ≤ 需求 $%.0f（Σnotional $%.0f/悲观档20）｜equity $%.0f",
				keydomain.BareOf(venueKey), freeMargin, need, sumNotional, equity))
		if aerr != nil {
			log.Printf("[freemargin] %s critical alert write: %v", venueKey, aerr)
		}
		return
	}
	if level == "warn" {
		aerr := st.InsertAlert(qctx, nil, "warn",
			fmt.Sprintf("MT5 保证金水位偏低：账户 %s freeMargin $%.0f < max(0.2×equity $%.0f, 1.5×需求 $%.0f（Σnotional $%.0f/20））",
				keydomain.BareOf(venueKey), freeMargin, equity, need, sumNotional))
		if aerr != nil {
			log.Printf("[freemargin] %s warn alert write: %v", venueKey, aerr)
		}
	}
}

// pessimisticLeverageD006 与 exec.pessimisticLeverage 同值 SSOT（包私有不可跨包引用
// 常量——两侧同源钉死 20，业主拍板值）。
const pessimisticLeverageD006 = 20.0

// freeMarginRiskLevel D-006 S2 双因子判级（纯函数可测）：
// fm ≤ need → critical；fm < max(0.2×equity, 1.5×need) → warn；否则 ""。
func freeMarginRiskLevel(freeMargin, equity, need float64) string {
	if freeMargin <= need {
		return "critical"
	}
	if freeMargin < math.Max(0.2*equity, 1.5*need) {
		return "warn"
	}
	return ""
}
