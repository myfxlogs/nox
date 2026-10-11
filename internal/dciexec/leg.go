// leg.go —— D-191 R3/R6：腿参数+strike %OTM 闸+前置赎回缺口步。
package dciexec

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"arb/internal/store/pgstore"
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

// ratioOf 腿→界比率指针（nil=约束行缺——调用方落 constraint 缺口）。
func (l leg) ratioOf(call, put *float64) *float64 {
	if l.optType == "CALL" {
		return call
	}
	return put
}

// strikeBoundOf 腿→约束 subject（缺界缺口键直用）。
func (l leg) boundSubject() string {
	if l.optType == "CALL" {
		return "dci_call_otm_bound"
	}
	return "dci_put_otm_bound"
}

// leg 腿参数（CALL=持币卖高 covered-call；PUT=持 USDT 买低 cash-put）。
type leg struct {
	name      string // CALL|PUT（order 账口径）
	optType   string
	investCcy string // 投入币（CALL=BTC、PUT=USDT）
	qtyCcy    string // 余额读径资产
	tmpl      string
}

var (
	callLeg = leg{name: "CALL", optType: "CALL", investCcy: "BTC", qtyCcy: "BTC",
		tmpl: "dci_sell_high_covered_call"}
	putLeg = leg{name: "PUT", optType: "PUT", investCcy: "USDT", qtyCcy: "USDT",
		tmpl: "dci_buy_low"}
)

// strikePass %OTM 闸（bound=spot×约束界，调用方算好传入）：CALL→strike≥bound、
// PUT→strike≤bound（strike 缺=不达标）。
func (l leg) strikePass(q *pgstore.CexDCIQuote, bound float64) bool {
	if q.Strike == nil {
		return false
	}
	if l.optType == "CALL" {
		return *q.Strike >= bound
	}
	return *q.Strike <= bound
}

// StrikeBound %OTM 闸界=spot×约束比率（R8 SQL 下推源；R9 比率由约束表供）。
func StrikeBound(spot, ratio float64) float64 { return spot * ratio }

// SubscribeBody 申购参数构造（D-191-R10①：venue 要 id|orderId|depositAmount|
// autoCompoundPlan——原 projectId/lots 参数名错代 HTTP 400 -1102；签名=HMAC(query) 同径）。
func (e *Executor) SubscribeBody(productRef, orderRef string, lots float64, at time.Time) (query, url string) {
	// depositAmount 按 purchase_decimal floor 后值原样发（-1=去尾零，dec0→"541"）。
	q := fmt.Sprintf("id=%s&orderId=%s&depositAmount=%s&autoCompoundPlan=NONE&timestamp=%d&recvWindow=5000",
		productRef, orderRef, strconv.FormatFloat(lots, 'f', -1, 64), at.UnixMilli())
	return q, fmt.Sprintf("%s/v1/dci/product/subscribe?%s&signature=%s",
		strings.TrimRight(e.Sapi, "/"), q, sign(e.BnSec, q))
}

// tenorCapOf 期限上限=custody_redeemable.value_num（票面钦定读 90；行在无数值→14）。
func tenorCapOf(cons []pgstore.CapabilityConstraint) (share *float64, cap int) {
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

func (l leg) boundText(bound float64) string {
	if l.optType == "CALL" {
		return fmt.Sprintf("≥%.0f", bound)
	}
	return fmt.Sprintf("≤%.0f", bound)
}

func delivDay(q *pgstore.CexDCIQuote) string {
	if q.DeliveryTs != nil {
		return q.DeliveryTs.UTC().Format("20060102")
	}
	return "na"
}

func tenor(q *pgstore.CexDCIQuote) int {
	if q.TenorDays != nil {
		return *q.TenorDays
	}
	return 0
}

// venueErrKind venue 败因分类（D-196④）：HTTP 401/403/权限码→permission（真业主
// 请求「请开通权限」）；400/参数/产品态→runtime（我方参数/产品病灶=「系统侧病灶」）。
func venueErrKind(err error) string {
	s := err.Error()
	for _, k := range []string{"HTTP 401", "HTTP 403", "-2014", "-2015", "permissions", "Permission"} {
		if strings.Contains(s, k) {
			return "permission"
		}
	}
	return "runtime"
}

// floorTo depositAmount 向 purchase_decimal 截位（R11：floor 不 round——多交禁、
// 界外如实败不补不截）。
func floorTo(v float64, dec int) float64 {
	m := math.Pow10(dec)
	return math.Floor(v*m) / m
}

func amtTxt(v *float64) string {
	if v == nil {
		return "n/a"
	}
	return strconv.FormatFloat(*v, 'f', -1, 64)
}
