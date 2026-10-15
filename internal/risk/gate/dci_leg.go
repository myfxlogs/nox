// dci_leg.go —— DCI 腿参数+%OTM/期限 自带闸（dciexec/leg.go 拆出，D-212 拆搬；纯移动零语义）。
package gate

import (
	"fmt"

	"nox/internal/store/pgstore"
)

// %OTM 闸界读约束表（D-191-R9：字面量入库——subject=dci_call_otm_bound/
// dci_put_otm_bound；行缺=nil 由调用方落 constraint 缺口，同 custody_share 例）。
// OTMBoundRatios 返回 (CALL 界, PUT 界)；界语义=乘现货的比率。
func OTMBoundRatios(cons []pgstore.CapabilityConstraint) (call, put *float64) {
	for _, c := range cons {
		switch c.Subject {
		case "dci_call_otm_bound":
			call = c.ValueNum
		case "dci_put_otm_bound":
			put = c.ValueNum
		}
	}
	return
}

// RatioOf 腿→界比率指针（nil=约束行缺——调用方落 constraint 缺口）。
func (l DCILeg) RatioOf(call, put *float64) *float64 {
	if l.OptType == "CALL" {
		return call
	}
	return put
}

// BoundSubject 腿→约束 subject（D-526 尾险闸 σ 界键；缺界缺口键直用）。
func (l DCILeg) BoundSubject() string {
	if l.OptType == "CALL" {
		return "dci_call_otm_k"
	}
	return "dci_put_otm_k"
}

// DCILeg 腿参数（CALL=持币卖高 covered-call；PUT=持 USDT 买低 cash-put）。
type DCILeg struct {
	Name      string // CALL|PUT（order 账口径）
	OptType   string
	InvestCcy string // 投入币（CALL=BTC、PUT=USDT）
	QtyCcy    string // 余额读径资产
	Tmpl      string
}

var (
	CallLeg = DCILeg{Name: "CALL", OptType: "CALL", InvestCcy: "BTC", QtyCcy: "BTC",
		Tmpl: "dci_sell_high_covered_call"}
	PutLeg = DCILeg{Name: "PUT", OptType: "PUT", InvestCcy: "USDT", QtyCcy: "USDT",
		Tmpl: "dci_buy_low"}
)

// StrikePass %OTM 闸（bound=spot×约束界，调用方算好传入）：CALL→strike≥bound、
// PUT→strike≤bound（strike 缺=不达标）。
func (l DCILeg) StrikePass(q *pgstore.CexDCIQuote, bound float64) bool {
	if q.Strike == nil {
		return false
	}
	if l.OptType == "CALL" {
		return *q.Strike >= bound
	}
	return *q.Strike <= bound
}

// StrikeBound %OTM 闸界=spot×约束比率（R8 SQL 下推源；R9 比率由约束表供）。
func StrikeBound(spot, ratio float64) float64 { return spot * ratio }

// TenorCapOf 期限上限=custody_redeemable.value_num（票面钦定读 90；行在无数值→14）。
func TenorCapOf(cons []pgstore.CapabilityConstraint) (share *float64, cap int) {
	cap = 30 // 无 redeemable 约束=非托管语义，保守中间档
	for _, c := range cons {
		switch c.Subject {
		case "custody_share":
			share = c.ValueNum
		case "custody_redeemable":
			cap = 14
			if c.ValueNum != nil {
				cap = int(*c.ValueNum)
			}
		}
	}
	return share, cap
}

// CexExposurePctOf CEX 侧权益敞口上限（AGENTS §1 硬线誊录行）——
// subject=cex_exposure_pct qualifier=max 的 value_num；行缺→nil（调用方
// fail-closed 落 constraint 缺口，同 custody_share 例）。
func CexExposurePctOf(cons []pgstore.CapabilityConstraint) *float64 {
	for _, c := range cons {
		if c.Subject == "cex_exposure_pct" && c.Qualifier == "max" {
			return c.ValueNum
		}
	}
	return nil
}

func (l DCILeg) BoundText(bound float64) string {
	if l.OptType == "CALL" {
		return fmt.Sprintf("≥%.0f", bound)
	}
	return fmt.Sprintf("≤%.0f", bound)
}

func DelivDay(q *pgstore.CexDCIQuote) string {
	if q.DeliveryTs != nil {
		return q.DeliveryTs.UTC().Format("20060102")
	}
	return "na"
}

func DCITenor(q *pgstore.CexDCIQuote) int {
	if q.TenorDays != nil {
		return *q.TenorDays
	}
	return 0
}
