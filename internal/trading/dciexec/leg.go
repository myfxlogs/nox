// leg.go —— D-191 R3/R6：申购参数/venue 败因/截位件（闸件迁 internal/risk/gate/dci_leg.go，D-212 拆搬）。
package dciexec

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// SubscribeBody 申购参数构造（D-191-R10①：venue 要 id|orderId|depositAmount|
// autoCompoundPlan——原 projectId/lots 参数名错代 HTTP 400 -1102；签名=HMAC(query) 同径）。
func (e *Executor) SubscribeBody(productRef, orderRef string, lots float64, at time.Time) (query, url string) {
	// depositAmount 按 purchase_decimal floor 后值原样发（-1=去尾零，dec0→"541"）。
	q := fmt.Sprintf("id=%s&orderId=%s&depositAmount=%s&autoCompoundPlan=NONE&timestamp=%d&recvWindow=5000",
		productRef, orderRef, strconv.FormatFloat(lots, 'f', -1, 64), at.UnixMilli())
	return q, fmt.Sprintf("%s/v1/dci/product/subscribe?%s&signature=%s",
		strings.TrimRight(e.Sapi, "/"), q, sign(e.BnSec, q))
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

// decOf D-191-R12：depositAmount floor 位数真源=该档 min_amount 小数位
// （FormatFloat 'f' -1 实际小数位；purchase_decimal 不作发参源只留采集档）。
func decOf(v float64) int {
	s := strconv.FormatFloat(v, 'f', -1, 64)
	if i := strings.IndexByte(s, '.'); i >= 0 {
		return len(s) - i - 1
	}
	return 0
}

func amtTxt(v *float64) string {
	if v == nil {
		return "n/a"
	}
	return strconv.FormatFloat(*v, 'f', -1, 64)
}
