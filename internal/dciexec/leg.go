// leg.go —— D-191 R3/R6：腿参数+strike %OTM 闸+前置赎回缺口步。
package dciexec

import (
	"fmt"
	"strings"
	"time"

	"arb/internal/store/pgstore"
)

// %OTM 闸（票面钦定：卖高 CALL≥×1.10、买低 PUT≤×0.93）。
const (
	otmCallRatio = 1.10
	otmPutRatio  = 0.93
)

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

// strikePass %OTM 闸：CALL→strike≥spot×1.10、PUT→strike≤spot×0.93（strike 缺=不达标）。
func (l leg) strikePass(q *pgstore.CexDCIQuote, spot float64) bool {
	if q.Strike == nil {
		return false
	}
	if l.optType == "CALL" {
		return *q.Strike >= spot*otmCallRatio
	}
	return *q.Strike <= spot*otmPutRatio
}

// SubscribeBody 申购参数构造（projectId+lots+时间戳；签名=HMAC(query)）。
func (e *Executor) SubscribeBody(projectID string, lots float64, at time.Time) (query, url string) {
	q := fmt.Sprintf("projectId=%s&lots=%.8f&timestamp=%d&recvWindow=5000",
		projectID, lots, at.UnixMilli())
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

func (l leg) boundText(spot float64) string {
	if l.optType == "CALL" {
		return fmt.Sprintf("≥%.0f", spot*otmCallRatio)
	}
	return fmt.Sprintf("≤%.0f", spot*otmPutRatio)
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
