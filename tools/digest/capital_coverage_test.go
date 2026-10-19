// capital_coverage_test.go —— D-356 S3：资金不眠覆盖率段渲染锚（纯函数，无 DB）。
// 精确断言：覆盖率/加权年化两数 + 未采行 + 纸面不入汇总 + 空态 + 除零态 + D-258 能量 APR 公式。
package main

import (
	"strings"
	"testing"
)

func f64(v float64) *float64 { return &v }

// TestD356_RenderCapitalCoverage 主锚（D-629 断代——TRX/CMC 翻业绩外·自营盘：
// RealMoney/Productive=false 不入总资金/在产/加权年化；历史行旧口径 48000/40500/84.4%
// 4.02% 断代留档）：TRX 30000（业绩外仅展示）+ 外部 10000 持仓中 + venue 净值 8000
// + 占用 500@5 → 总资金 18000（Σvenue 净值）、在产 10500、覆盖率 58.3%、加权 5.00%
// （占用行独算）；纸面 $50000 不入总资金；未采固定行。
func TestD356_RenderCapitalCoverage(t *testing.T) {
	rows := []CapitalCoverageRow{
		{Pool: "TRX 质押+投票+能量", BalanceUSD: f64(30000), BalanceCcy: "USD", Status: "业绩外·自营盘",
			Note: "业绩外·自营盘（D-629：不入总资金/在产/加权年化）；投票 3.30%+能量租赁 0.70%"},
		{Pool: "外部账户", BalanceUSD: f64(10000), BalanceCcy: "USD", Status: "$123.45",
			Note: "外部持仓监控域；手动持仓中", Productive: true, RealMoney: true},
		{Pool: "binance·合约", BalanceUSD: f64(8000), BalanceCcy: "USDT", Status: "部分占用",
			Note: "快照 1h 前，可用 7500.00", RealMoney: true},
		{Pool: "保证金占用", BalanceUSD: f64(500), BalanceCcy: "USD", Status: "占用中",
			APRPct: f64(5.0), Note: "账户级占用（含同账户手动仓，腿级切分不可得）；占用明细 binance·合约 $500.00；名义额参考 $10000.00（不入合计）",
			Productive: true, RealMoney: false},
		{Pool: "系统持仓（纸面）", BalanceUSD: f64(50000), BalanceCcy: "USD", Status: "在跑（纸面）",
			Note: "非真金不入汇总", Productive: false, RealMoney: false},
		{Pool: "CEX/MT 账户余额", Status: "未采", Note: "采集未立项——余额未入库，本行为决策提示占位"},
	}
	got := RenderCapitalCoverageSection(rows)
	for _, want := range []string{
		"资金不眠覆盖率（快照口径",
		"| TRX 质押+投票+能量 | 30000.00 USD | 业绩外·自营盘 | — |",
		"| 外部账户 | 10000.00 USD | $123.45 | — | 外部持仓监控域；手动持仓中 |",
		"| binance·合约 | 8000.00 USDT | 部分占用 | — |",
		"| 保证金占用 | 500.00 USD | 占用中 | 5.00 | 账户级占用（含同账户手动仓，腿级切分不可得）；占用明细 binance·合约 $500.00；名义额参考 $10000.00（不入合计） |",
		"| 系统持仓（纸面） | 50000.00 USD | 在跑（纸面） | — |",
		"总资金 $18000.00 | 在产 $10500.00（覆盖率 58.3%）| 加权年化 5.00%",
		"| CEX/MT 账户余额 | — | 未采 |",
		"口径：快照时点在产",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("段缺 %q:\n%s", want, got)
		}
	}
}

// TestD356_RenderCapitalCoverage_IdleExternal 外部现金闲置 → 不计入产分子（D-127 口径；
// D-629 断代：TRX 翻业绩外）→ 占用独入分子 500/18000=2.8%（非饱和值锚）。
func TestD356_RenderCapitalCoverage_IdleExternal(t *testing.T) {
	rows := []CapitalCoverageRow{
		{Pool: "TRX 质押+投票+能量", BalanceUSD: f64(30000), BalanceCcy: "USD", Status: "业绩外·自营盘"},
		{Pool: "外部账户", BalanceUSD: f64(10000), BalanceCcy: "USD", Status: "现金闲置", RealMoney: true},
		{Pool: "binance·合约", BalanceUSD: f64(8000), BalanceCcy: "USDT", Status: "闲置", RealMoney: true},
		{Pool: "保证金占用", BalanceUSD: f64(500), BalanceCcy: "USD", Status: "占用中",
			APRPct: f64(5.0), Productive: true, RealMoney: false},
		{Pool: "CEX/MT 账户余额", Status: "未采"},
	}
	got := RenderCapitalCoverageSection(rows)
	if !strings.Contains(got, "总资金 $18000.00 | 在产 $500.00（覆盖率 2.8%）") {
		t.Errorf("在产应 (500)/18000=2.8%%:\n%s", got)
	}
	// 外部/venue 闲置无年化：加权年化=占用独算 (500×5)/500=5.00
	if !strings.Contains(got, "加权年化 5.00%") {
		t.Errorf("加权年化应 5.00%%:\n%s", got)
	}
}

// TestD356_RenderCapitalCoverage_Empty 四数据源全缺 → 「数据源全缺」诚实空态（不猜零）。
func TestD356_RenderCapitalCoverage_Empty(t *testing.T) {
	got := RenderCapitalCoverageSection(nil)
	if !strings.Contains(got, "数据源全缺") {
		t.Errorf("空行集应显示数据源全缺:\n%s", got)
	}
	if strings.Contains(got, "| 资金池") {
		t.Errorf("空态不应渲染表格:\n%s", got)
	}
}

// TestD356_RenderCapitalCoverage_ZeroTotal 余额全缺（价缺失/未采）→ 汇总除零「—」不猜。
func TestD356_RenderCapitalCoverage_ZeroTotal(t *testing.T) {
	rows := []CapitalCoverageRow{
		{Pool: "TRX 质押+投票+能量", Status: "业绩外·自营盘", Note: "TRX 数量 35000（无 USD 价）"},
		{Pool: "CEX/MT 账户余额", Status: "未采"},
	}
	got := RenderCapitalCoverageSection(rows)
	if !strings.Contains(got, "总资金 — | 在产 —（覆盖率 —）| 加权年化 —") {
		t.Errorf("总资金=0 应显示 —:\n%s", got)
	}
}

// TestD356_EnergyAPRFormula D-258 口径公式锚：日均 10 TRX×365/在押 36500×100=10.00%。
func TestD356_EnergyAPRFormula(t *testing.T) {
	if got := energyAPRFromSample(300, 30, 36500); got < 9.9999 || got > 10.0001 {
		t.Errorf("energy APR = %f, want 10.0", got)
	}
}
