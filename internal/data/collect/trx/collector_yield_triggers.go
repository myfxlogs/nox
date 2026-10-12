// S1/S2：扳机只喊不动手（L2 业主）——InsertAlert 告警，不触发任何执行。
// 隔夜/净值压力/合成年化三件随 CMC 域剔除（fin_cmc_* 不搬，D-225 族件剔除）；
// 余存活扳机：S2-1 能量 30d 实测年化 <8% → critical。
//
// 不动 D-269 改投逻辑；金额展示只在后端聚合（D-047）。
package trx

import (
	"context"
	"fmt"
	"log"
	"time"
)

// checkYieldTriggers D-278 S1/S2：扳机入口——TxsCollector.Poll 有新行后调。
func (c *TxsCollector) checkYieldTriggers(ctx context.Context) {
	now := time.Now().UTC()
	today := now.Format("2006-01-02")

	// S2-1：能量 30d 实测年化 <8% → critical
	c.checkEnergy30dTrigger(ctx, now, today)
}

// checkEnergy30dTrigger S2-1：能量 30d 实测年化 <8% → critical。
// 年化 = (30d rental 合计 TRX / sampleDays × 365) / 在押 TRX × 100。
func (c *TxsCollector) checkEnergy30dTrigger(ctx context.Context, now time.Time, today string) {
	from := now.AddDate(0, 0, -30)
	rentalSun, err := c.store.SumTRXTxRentalByRange(ctx, from, now)
	if err != nil {
		log.Printf("[trx-yield] energy 30d check skipped: sum range: %v", err)
		return
	}
	firstTs, ok, err := c.store.FirstTRXTxRentalTsInRange(ctx, from, now)
	if err != nil || !ok {
		return // 冷启动不猜
	}
	sampleDays := int(now.Sub(firstTs)/(24*time.Hour)) + 1
	if sampleDays < 30 {
		return // 样本不足
	}
	acct, err := c.store.LastTRXAccount(ctx)
	if err != nil || acct.EnergyDelegated == 0 {
		return
	}
	// 过渡期护栏：30d 窗内 energy_delegated 变化>20% → 分母失真
	minE, maxE, err := c.store.EnergyDelegatedRangeInWindow(ctx, from, now)
	if err == nil && minE > 0 && maxE > minE*12/10 {
		return
	}
	rentalTRX := float64(rentalSun) / 1e6
	energyTRX := float64(acct.EnergyDelegated) / 1e6
	apr := (rentalTRX / float64(sampleDays) * 365) / energyTRX * 100
	if apr >= 8.0 {
		return
	}
	if c.lastEnergy30dAlertDay == today {
		return
	}
	msg := fmt.Sprintf("[trx-yield S2] 能量 30d 实测年化 %.2f%% < 8%%（在押 %.0f TRX，样本 %d 天）——能量收益率跌破临界线",
		apr, energyTRX, sampleDays)
	if err := c.store.InsertAlert(ctx, nil, "critical", msg); err != nil {
		log.Printf("[trx-yield] energy 30d alert insert failed: %v", err)
		return
	}
	c.lastEnergy30dAlertDay = today
	log.Printf("[trx-yield S2] energy 30d below 8%%: apr=%.2f%% → critical alert sent", apr)
}
