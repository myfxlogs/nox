// capital_coverage_render.go —— D-356 S2：资金不眠覆盖率段渲染（纯函数，数据结构在 capital_coverage.go）。
package main

import (
	"fmt"
	"strings"
)

// ccyOr D-130 域3：币种缺省回落 USD（历史行口径）。
func ccyOr(ccy string) string {
	if ccy == "" {
		return "USD"
	}
	return ccy
}

// RenderCapitalCoverageSection D-356 S2：「资金不眠覆盖率（快照口径）」段。
// 空切片（四数据源全缺）→「数据源全缺」诚实空态一行。汇总行（D-127 拆门）：
// 总资金=RealMoney∧余额非 nil 和（占用行=净值子集视图 RealMoney=false 不入，防双重计入）；
// 在产=Productive∧余额非 nil 和（占用行入分子）；覆盖率=在产/总资金；
// 加权年化=Σ(余额×年化)/Σ有年化有余额行（占用行按占用额加权）；总资金=0/无年化行 → 「—」（除零不猜）。
func RenderCapitalCoverageSection(rows []CapitalCoverageRow) string {
	var buf strings.Builder
	buf.WriteString("\n## 资金不眠覆盖率（快照口径，D-356）\n\n")
	if len(rows) == 0 {
		buf.WriteString("数据源全缺（TRX/系统持仓/资金快照均无行——不猜零）\n")
		return buf.String()
	}
	buf.WriteString("| 资金池 | 余额 | 状态 | 年化 % | 说明 |\n")
	buf.WriteString("|---|---|---|---|---|\n")
	var total, productive, wSum, wBal float64
	for _, r := range rows {
		bal := "—"
		if r.BalanceUSD != nil {
			bal = fmt.Sprintf("%.2f %s", *r.BalanceUSD, ccyOr(r.BalanceCcy)) // D-130 域3：原币直显
			if r.RealMoney && balanceCcyFoldable(r.BalanceCcy) {
				total += *r.BalanceUSD
			}
			if r.Productive {
				productive += *r.BalanceUSD
			}
			if r.APRPct != nil {
				wSum += *r.BalanceUSD * *r.APRPct
				wBal += *r.BalanceUSD
			}
		}
		apr := "—"
		if r.APRPct != nil {
			apr = fmt.Sprintf("%.2f", *r.APRPct)
		} else if r.APRNilText != "" {
			apr = r.APRNilText
		}
		buf.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %s |\n", r.Pool, bal, r.Status, apr, r.Note))
	}
	if total > 0 {
		weighted := "—"
		if wBal > 0 {
			weighted = fmt.Sprintf("%.2f%%", wSum/wBal)
		}
		buf.WriteString(fmt.Sprintf("\n总资金 $%.2f | 在产 $%.2f（覆盖率 %.1f%%）| 加权年化 %s\n",
			total, productive, productive/total*100, weighted))
	} else {
		buf.WriteString("\n总资金 — | 在产 —（覆盖率 —）| 加权年化 —（总资金=0，除零不猜）\n")
	}
	buf.WriteString("\n> 口径：快照时点在产=TRX 在赚+保证金占用（非时序加权，日级趋势看历史行）；保证金占用=Σ(equity−free_margin) 账户级（含同账户手动仓、腿级切分不可得），可用额未采→不计（禁 eq−0 假满占）；总资金=Σ账户净值+TRX（D-127 业主令：杠杆名义额退出合计，仅作占用行 note 参考值）；加权年化分母=有年化行余额（占用行按占用额加权；手动仓/未采/纸面/venue 现金不进）；纸面持仓非真金不入汇总；venue 账户现金=闲置不计在产（睡钱暴露；CEX=USDT 标余额，USDT/LDUSDT 计、杂币不折算；MT=USD 同视）；CFD 周末闭市持仓中≠空转；占用行年化=position_daily_income 近7日日均息差（TRX 收益×快照价+利息，浮动盈亏不计）按名义额折算；能量租赁年化=D-258 30d 口径。\n")
	return buf.String()
}
