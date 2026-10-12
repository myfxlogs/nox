// D-278 S1/S2：扳机只喊不动手（L2 业主）——InsertAlert 告警，不触发任何执行。
// 扳机四件+ S1 告警两件，各 1 条/天节流（内存日期戳）：
//   - S2-1 能量 30d 实测年化 <8% → critical
//   - S2-2 空单隔夜 7d 衰退 >50% → warning
//   - S2-3 隔夜连续 2 日为负 → warning
//   - S2-4 净值压力（TRX +30% 场景投影 < $6K）→ warning
//   - S1-A 30d 合成年化 <12% → warning
//   - S1-B 隔夜连续 2 日为负 → warning（与 S2-3 同源，合并去重）
//
// 不动 D-269 改投逻辑；金额展示只在后端聚合（D-047）。
package trx

import (
	"context"
	"fmt"
	"log"
	"time"
)

// trxPoolPrincipalUSD D-278 S1/S2：年化分母=$36.5K 池口径常量（与底稿可比，AF4）。
const trxPoolPrincipalUSD = 36500.0

// trxShortInstrument D-278 S2：TRX 空单 instrument ID（X-AFQUL，单空单事实）。
const trxShortInstrument = "X-AFQUL"

// trxMarginPressureUSD D-278 S2：净值压力池边界 $6K（TRX +30% 场景投影基准）。
const trxMarginPressureUSD = 6000.0

// checkYieldTriggers D-278 S1/S2：扳机入口——TxsCollector.Poll 有新行后调。
func (c *TxsCollector) checkYieldTriggers(ctx context.Context) {
	now := time.Now().UTC()
	today := now.Format("2006-01-02")

	// S2-1：能量 30d 实测年化 <8% → critical
	c.checkEnergy30dTrigger(ctx, now, today)

	// S2-2 + S2-3 + S1-B：空单隔夜衰退 + 连续负值（共用隔夜日序列）
	c.checkOvernightTriggers(ctx, now, today)

	// S2-4：净值压力（TRX +30% 场景）
	c.checkPressureTrigger(ctx, now, today)

	// S1-A：30d 合成年化 <12% → warning
	c.checkS1CombinedTrigger(ctx, now, today)
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

// checkOvernightTriggers S2-2 + S2-3/S1-B：空单隔夜 7d 衰退 >50% + 连续 2 日为负。
func (c *TxsCollector) checkOvernightTriggers(ctx context.Context, now time.Time, today string) {
	// 取最近 14d 隔夜日序列（前 7d vs 后 7d 对比 + 连续负值检查）
	from := now.AddDate(0, 0, -14)
	yieldDays, err := c.store.ListTRXYieldByDay(ctx, from, now)
	if err != nil || len(yieldDays) < 2 {
		return
	}

	// S2-2：近 7d 隔夜合计 vs 前 7d 隔夜合计，衰退 >50%
	var recent7, prior7 float64
	cutoff := now.AddDate(0, 0, -7)
	for _, yd := range yieldDays {
		if yd.Day.After(cutoff) || yd.Day.Equal(cutoff) {
			recent7 += yd.InterestUSD
		} else {
			prior7 += yd.InterestUSD
		}
	}
	if prior7 > 0 && recent7 < prior7*0.5 {
		if c.lastOvernightDeclineDay != today {
			msg := fmt.Sprintf("[trx-yield S2] 空单隔夜 7d 衰退 >50%%：前 7d=%.2f USD → 近 7d=%.2f USD（衰退 %.0f%%）",
				prior7, recent7, (1-recent7/prior7)*100)
			if err := c.store.InsertAlert(ctx, nil, "warn", msg); err != nil {
				log.Printf("[trx-yield] overnight decline alert insert failed: %v", err)
			} else {
				c.lastOvernightDeclineDay = today
				log.Printf("[trx-yield S2] overnight 7d decline >50%%: prior=%.2f recent=%.2f → warning sent", prior7, recent7)
			}
		}
	}

	// S2-3 / S1-B：连续 2 日隔夜为负 → warning（合并去重，共用 lastOvernightNegDay）
	if len(yieldDays) >= 2 {
		last := yieldDays[len(yieldDays)-1]
		prev := yieldDays[len(yieldDays)-2]
		if last.InterestUSD < 0 && prev.InterestUSD < 0 {
			if c.lastOvernightNegDay != today {
				msg := fmt.Sprintf("[trx-yield S1/S2] 空单隔夜连续 2 日为负：%s=%.4f USD，%s=%.4f USD",
					prev.Day.Format("2006-01-02"), prev.InterestUSD,
					last.Day.Format("2006-01-02"), last.InterestUSD)
				if err := c.store.InsertAlert(ctx, nil, "warn", msg); err != nil {
					log.Printf("[trx-yield] overnight negative alert insert failed: %v", err)
				} else {
					c.lastOvernightNegDay = today
					log.Printf("[trx-yield S1/S2] overnight consecutive negative → warning sent")
				}
			}
		}
	}
}

// checkPressureTrigger S2-4：净值压力——TRX +30% 场景投影 < $6K → warning。
// 投影损失 = 空单数量 × 现价 × 30%；投影净值 = 当前净值 - 投影损失。
// 阈值=$6K 池边界（D-278 设计 §S2）；复用 InsertAlert（D-252 告警机制同源）。
func (c *TxsCollector) checkPressureTrigger(ctx context.Context, now time.Time, today string) {
	pos, err := c.store.LastCMCPositionByInstrument(ctx, trxShortInstrument)
	if err != nil || pos.EvaluationBidPrice <= 0 || pos.Quantity <= 0 {
		return
	}
	sum, err := c.store.LastCMCAccountSummary(ctx)
	if err != nil {
		return
	}
	projectedLoss := pos.Quantity * pos.EvaluationBidPrice * 0.30
	projectedEquity := sum.TotalAccountValue - projectedLoss
	if projectedEquity >= trxMarginPressureUSD {
		return
	}
	if c.lastPressureDay == today {
		return
	}
	msg := fmt.Sprintf("[trx-yield S2] 净值压力：TRX +30%% 场景投影净值 %.2f USD < $6K 池边界（当前净值 %.2f，投影损失 %.2f）",
		projectedEquity, sum.TotalAccountValue, projectedLoss)
	if err := c.store.InsertAlert(ctx, nil, "warn", msg); err != nil {
		log.Printf("[trx-yield] pressure alert insert failed: %v", err)
		return
	}
	c.lastPressureDay = today
	log.Printf("[trx-yield S2] pressure: projected equity %.2f < $6K → warning sent", projectedEquity)
}

// checkS1CombinedTrigger S1-A：30d 合成年化 <12% → warning。
// 合成年化 = (30d 三成分 USD 合计 / $36.5K) × 365 / sampleDays × 100。
func (c *TxsCollector) checkS1CombinedTrigger(ctx context.Context, now time.Time, today string) {
	from := now.AddDate(0, 0, -30)
	yieldDays, err := c.store.ListTRXYieldByDay(ctx, from, now)
	if err != nil || len(yieldDays) < 30 {
		return // 样本不足
	}
	// 现价（fin_cmc_positions.evaluation_bid_price）
	var price float64
	if pos, err := c.store.LastCMCPositionByInstrument(ctx, trxShortInstrument); err == nil {
		price = pos.EvaluationBidPrice
	}
	if price <= 0 {
		return // 无现价不评估
	}
	var sumUSD float64
	for _, yd := range yieldDays {
		p := price
		if yd.Price != nil {
			p = *yd.Price
		}
		sumUSD += (yd.EnergyTrx+yd.VotingTrx)*p + yd.InterestUSD
	}
	sampleDays := len(yieldDays)
	ann := (sumUSD / trxPoolPrincipalUSD) * 365.0 / float64(sampleDays) * 100
	if ann >= 12.0 {
		return
	}
	if c.lastS1CombinedDay == today {
		return
	}
	msg := fmt.Sprintf("[trx-yield S1] 30d 合成年化 %.2f%% < 12%%（样本 %d 天，合计 %.2f USD，分母 $36.5K）",
		ann, sampleDays, sumUSD)
	if err := c.store.InsertAlert(ctx, nil, "warn", msg); err != nil {
		log.Printf("[trx-yield] S1 combined alert insert failed: %v", err)
		return
	}
	c.lastS1CombinedDay = today
	log.Printf("[trx-yield S1] 30d combined below 12%%: ann=%.2f%% → warning sent", ann)
}
