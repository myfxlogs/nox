// service_capital_coverage_occupied.go —— D-127：保证金占用口径（名义额→占用额修复的占用域逻辑）。
// 占用=Σ(equity−free_margin) 账户级子集视图（MT 帧直带 Margin>0 优先）；行=「保证金占用」
// RealMoney=false 不入总资金分母（防净值双重计入）∧Productive=true 入在产分子。
// 口径 SSOT 与主文件头注同源；digest capital_coverage*.go 同口径双实现。
package dashboard

import (
	"context"
	"fmt"
	"strings"

	"arb/internal/capitalview"
	v1 "arb/internal/gen/dashboardv1"
)

// venueOcc D-127：账户占用聚合（占用=venue 净值中真被保证金占用的子集，USDT/USD 同视账户口径）。
type venueOcc struct {
	sum     float64  // Σ已知占用
	known   int      // 占用已知账户数
	missing int      // 可用额未采账户数（fail-closed 不入分子）
	items   []string // D-129：占用>0 账户明细（池名+额），收说明列
}

// accountOccupied D-127 新口径①：单账户占用=equity−free_margin；MT 帧直带 Margin>0 优先
// （帧值即 venue 原生占用）；fm≤0 视为未采（live 视图零值与快照 NULL 同型——禁 eq−0 假满占）；
// eq<fm 数据异常同样 fail-closed 不猜。资金面未出（ok=false）→ 未采。
func accountOccupied(f capitalview.Funds, ok bool) (float64, bool) {
	if !ok {
		return 0, false
	}
	if f.Margin > 0 {
		return f.Margin, true
	}
	if f.Available > 0 {
		occ := f.Equity - f.Available
		if occ < 0 {
			return 0, false
		}
		return occ, true
	}
	return 0, false
}

// marginOccupancyRow ③保证金占用（D-127 新口径，替代原「系统持仓」名义额行）：
// 值=Σ账户占用；名义额退出合计、降格 note 参考值；RealMoney=false=venue 净值子集视图
// （净值行已入分母，占用行不重复计）∧Productive=true 入在产分子；
// 年化仍按名义额折算（息差随名义产生），加权按占用额。
func marginOccupancyRow(ctx context.Context, s *Service, occ venueOcc) *v1.CapitalCoverageRow {
	r := covRow("保证金占用")
	r.BalanceCcy = "USD" // D-130 域3：占用=USDT/USD 同视口径
	r.Productive = true
	r.RealMoney = false
	var notional float64
	if ens, err := s.store.ActiveNotionalByEnv(ctx); err == nil {
		for _, e := range ens {
			notional += e.NotionalUSD // D-360 S4：查询已滤仅 live
		}
	}
	avg, hasAvg, err := s.store.AvgDailyIncomeCarry7d(ctx)
	if err != nil {
		hasAvg = false
	}
	if occ.known > 0 {
		r.BalanceUsd = occ.sum
		r.HasBalance = true
		r.Status = "占用中"
	} else if notional > 0 {
		r.Status = "占用未知" // 有仓但可用额全未采——fail-closed 不入分子（防虚胖）
	} else {
		r.Status = "零占用"
	}
	note := "账户级占用（含同账户手动仓，腿级切分不可得）"
	if len(occ.items) > 0 { // D-129：占用明细收说明列（hint 字段摘除）
		note += "；占用明细 " + strings.Join(occ.items, " · ")
	}
	if occ.missing > 0 {
		note += fmt.Sprintf("；%d 账户可用额未采不计", occ.missing)
	}
	if notional > 0 {
		note += fmt.Sprintf("；名义额参考 $%.2f（不入合计）", notional)
		if hasAvg {
			r.AprPct = avg * 365 / notional * 100
			r.HasApr = true
		} else {
			r.AprText = "新开仓"
		}
	} else if occ.known == 0 {
		note = "无活跃仓·无账户占用"
	}
	r.Note = note
	return r
}
