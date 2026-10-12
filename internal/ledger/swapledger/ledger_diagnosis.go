// swapledger 诊断类型与方法（从 ledger_entries.go 拆出，保 ledger_entries.go ≤300 行）。
package swapledger

import (
	"context"
	"time"

	"arb/internal/data/fact"
	"arb/internal/store/pgstore"
)

// LegSwapDiagnosis D-193-A 扩包 S3：leg_swapAmount 诊断结果（只读探针用）。
type LegSwapDiagnosis struct {
	PositionID     int64
	LegNo          int16
	Venue          string
	Side           string
	Canonical      string
	Day            time.Time
	FactKey        string  // 所查 fact 键 "canonical:venue"
	FactKind       string  // swap_long/swap_short/funding
	FactHit        bool    // fact 是否命中
	FactValue      float64 // fact.Value（命中时）
	FactTs         time.Time
	FactExpired    bool // Fact.Ts 超 factTTL
	NotionalHit    bool // usdPerLot 是否命中
	UsdPerLot      float64
	Notional       float64
	Multiplier     float64
	MultiplierOK   bool
	NonSettleVenue bool
	Ok             bool    // legSwapAmount 最终 ok
	Amount         float64 // legSwapAmount 最终 amount
	Reason         string  // ok=false 或 amount=0 的原因
}

// DiagnoseLegSwapAmount D-193-A 扩包 S3：只读诊断单腿 legSwapAmount 的中间状态。
// 对 478-481×leg1×9-2~9-4 输出每格 (ok, amount) + 所查 fact 键与命中状态。
// 结论三选一：ok=false（列出缺失 fact 键）/amount=0/正常（不该发生，停手回报）。
// 用生产库只读 DSN 由复审方跑；施工方用测试库验证逻辑。
func (l *Ledger) DiagnoseLegSwapAmount(ctx context.Context, pos pgstore.Position, leg pgstore.PositionLeg, day time.Time,
	longMap, shortMap, fundMap map[factKey]fact.Fact) LegSwapDiagnosis {
	canonical := canonicalFromOppID(pos.OpportunityID)
	isFunding := isFundingLeg(pos.Type, leg.Venue) // D-314：per-leg 判定（XASSET_CARRY perp 腿=funding）
	d := LegSwapDiagnosis{
		PositionID: pos.ID,
		LegNo:      leg.LegNo,
		Venue:      leg.Venue,
		Side:       leg.Side,
		Canonical:  canonical,
		Day:        day,
	}

	// 1. 取 swap/funding rate
	venueName := venueFromKey(leg.Venue)
	fk := factKey{canonical, venueName}
	d.FactKey = canonical + ":" + venueName
	var f fact.Fact
	var rateFound bool
	if isFunding {
		d.FactKind = fact.KindFunding
		f, rateFound = fundMap[fk]
	} else {
		if leg.Side == "BUY" {
			d.FactKind = fact.KindSwapLong
			f, rateFound = longMap[fk]
		} else {
			d.FactKind = fact.KindSwapShort
			f, rateFound = shortMap[fk]
		}
	}
	d.FactHit = rateFound
	if rateFound {
		d.FactValue = f.Value
		d.FactTs = f.Ts
		if l.factTTL > 0 && time.Since(f.Ts) > l.factTTL {
			d.FactExpired = true
			d.Ok = false
			d.Reason = "fact expired"
			return d
		}
	} else {
		d.Ok = false
		d.Reason = "fact not found (kind=" + d.FactKind + ")"
		return d
	}

	// 2. 取 notional
	side := "ask"
	if leg.Side == "SELL" {
		side = "bid"
	}
	usdPerLot, ok := l.notional.USDPerLotForLeg(canonical, leg.Venue, side)
	d.NotionalHit = ok
	d.UsdPerLot = usdPerLot
	if !ok || usdPerLot <= 0 {
		d.Ok = false
		d.Reason = "notional unavailable"
		return d
	}
	notional := usdPerLot * leg.Qty
	d.Notional = notional
	if notional <= 0 {
		d.Ok = false
		d.Reason = "notional <= 0"
		return d
	}

	// 3. 算金额
	if isFunding {
		d.Multiplier = 1
		d.MultiplierOK = true
		d.Amount = decimalAmount(f.Value, 100, DaysPerYear, notional, 1)
	} else {
		multiplier := settleMultiplier(leg.Venue, canonical, day, l.tripleDay)
		d.Multiplier = multiplier
		d.MultiplierOK = multiplier != 0
		if multiplier == 0 {
			d.Ok = false
			d.Reason = "settleMultiplier=0 (triple swap day unknown)"
			return d
		}
		d.Amount = decimalAmount(f.Value, 10000, 1, notional, multiplier)
	}

	// 4. nonSettleVenue 检查
	d.NonSettleVenue = l.nonSettleVenues[leg.Venue]
	if d.NonSettleVenue {
		d.Amount = 0
		d.Ok = true
		d.Reason = "non-settle venue (amount=0)"
		return d
	}

	d.Ok = true
	if d.Amount == 0 {
		d.Reason = "amount=0 (zero swap)"
	} else {
		d.Reason = "normal"
	}
	return d
}
