// dci_leg.go —— DCI 腿参数+σ 界/期限 自带闸（dciexec/leg.go 拆出，D-212 拆搬；
// D-526 返修：平界径 OTMBoundRatios/RatioOf 零调用清退，约束行迁 1028 删）。
package gate

import (
	"fmt"

	"nox/internal/store/pgstore"
)

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

// CustodyShareOf 托管可动份额上限读面（D-644 venue 粒度修）：
// venue 键 `custody_share@<venue>` 优先生效（okx 池=纯托管钱应×1.0，
// 非共享池语义再×0.34）；无 venue 键回落全局 `custody_share`
// （binance 共享池=自营+托管混存上限 0.34——向后兼容不 fail-closed）。
// venue="" 只查全局键。
func CustodyShareOf(cons []pgstore.CapabilityConstraint, venue string) *float64 {
	var global *float64
	for _, c := range cons {
		if c.ValueNum == nil {
			continue
		}
		if venue != "" && c.Subject == "custody_share@"+venue {
			return c.ValueNum
		}
		if c.Subject == "custody_share" {
			global = c.ValueNum
		}
	}
	return global
}

// TenorCapOf 期限上限=custody_redeemable.value_num（票面钦定读 90；行在无数值→14）。
// share 返回=全局键（venue 粒度读面走 CustodyShareOf——binance 共享池语义）。
func TenorCapOf(cons []pgstore.CapabilityConstraint) (share *float64, cap int) {
	cap = 30 // 无 redeemable 约束=非托管语义，保守中间档
	for _, c := range cons {
		if c.Subject == "custody_redeemable" {
			cap = 14
			if c.ValueNum != nil {
				cap = int(*c.ValueNum)
			}
		}
	}
	return CustodyShareOf(cons, ""), cap
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
