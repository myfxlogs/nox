// exec.go —— D-191 R3：轮式 DCI 执行轨（binance SAPI 写径，covered-call+cash-put 双向）。
// 触发=业主「go」口令；strike 规则化 %OTM 闸（CALL≥spot×1.10/PUT≤spot×0.93，
// spot=执行时实时读）；期限上限=custody_redeemable.value_num；qty=币安实时余额
// ×custody_share（禁读 personal_positions 静态行）；无达标档→gap 如实报不降级凑单；
// 幂等=fin_dci_order.request_id UNIQUE 单次尝试不重试；失败→fin_condition_gap 请求。
package dciexec

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"arb/internal/store/pgstore"
)

// Store 执行面依赖（*pgstore.Store 实现；测试走 fake）。
type Store interface {
	ListCapabilityConstraints(ctx context.Context) ([]pgstore.CapabilityConstraint, error)
	ListDCIQuotes(ctx context.Context, venue, underlying, optType, investCcy string, maxTenorDays int) ([]pgstore.CexDCIQuote, error)
	CreateDCIOrder(ctx context.Context, o *pgstore.DCIOrder) (int64, bool, error)
	FinishDCIOrder(ctx context.Context, id int64, status, response string) error
	InsertPersonalPosition(ctx context.Context, asset, kind, priceSymbol, note string, qty *float64, openedAt *time.Time) (int64, error)
	OutstandingDCIQty(ctx context.Context, venue, investCcy string) (float64, error)
	ListOpenPersonalPositions(ctx context.Context) ([]pgstore.PersonalPosition, error)
	UpdatePersonalPositionQtyNote(ctx context.Context, id int64, qty float64, note string) error
	OpenConditionGap(ctx context.Context, g *pgstore.ConditionGap) (int64, error)
	RequestConditionGap(ctx context.Context, id int64) error
	InsertAlert(ctx context.Context, ruleID *int64, level, message string) error
}

// Executor binance DCI 申购执行器（凭据由装配侧从 exchange_configs 注入）。
type Executor struct {
	Store   Store
	BnKey   string
	BnSec   string
	Sapi    string // sapi base（申购写径）
	APIBase string // api base（余额/现货读径）
	Client  *http.Client
	Now     func() time.Time // 测试可覆
	// 测试注入面（nil=真实 HTTP 实现）：
	PriceFn   func(ctx context.Context, symbol string) (float64, error)
	BalanceFn func(ctx context.Context, asset string) (float64, error)
}

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

// sign HMAC-SHA256 hex（binance sapi 同法，包内独立实现 P3 不适用）。
func sign(secret, query string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(query))
	return hex.EncodeToString(mac.Sum(nil))
}

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

// Go 轮式申购主径（业主口令「go」→双腿各试：CALL 持币卖高 + PUT 持U买低；
// 一腿成败不阻他腿；约束/报价/幂等三段闸同 R2）。
func (e *Executor) Go(ctx context.Context) (string, error) {
	if e.BnKey == "" {
		return "", e.gap(ctx, callLeg, "credential", "binance:spot",
			"请配置 binance 凭据（DCI 申购写径）")
	}
	cons, err := e.Store.ListCapabilityConstraints(ctx)
	if err != nil {
		return "", err
	}
	share, tenorCap := tenorCapOf(cons)
	if share == nil {
		return "", e.gap(ctx, callLeg, "constraint", "custody_share",
			"请喂约束 custody_share（托管可动份额上限）")
	}
	// 现货执行时实时读（strike %OTM 闸源）。
	spot, err := e.spotPrice(ctx, "BTCUSDT")
	if err != nil {
		return "", e.gap(ctx, callLeg, "permission", "dual_quote",
			"binance BTC 现货价读取失败：%v", err)
	}
	var msgs []string
	for _, l := range []leg{callLeg, putLeg} {
		msg, err := e.tryLeg(ctx, l, *share, tenorCap, spot)
		if err != nil {
			msgs = append(msgs, fmt.Sprintf("%s腿未执行：%v", l.name, err))
			continue
		}
		msgs = append(msgs, msg)
	}
	return strings.Join(msgs, "；"), nil
}

// tryLeg 单腿：余额×share→qty、候选档 strike 闸→最优、幂等→签名 POST→回执。
func (e *Executor) tryLeg(ctx context.Context, l leg, share float64, tenorCap int, spot float64) (string, error) {
	bal, err := e.freeBalance(ctx, l.qtyCcy)
	if err != nil {
		return "", fmt.Errorf("%s 余额读败：%w", l.qtyCcy, err)
	}
	qty := bal * share
	if qty <= 0 {
		return fmt.Sprintf("%s腿跳过（%s 可动=0）", l.name, l.qtyCcy), nil
	}
	qs, err := e.Store.ListDCIQuotes(ctx, "binance", "BTC", l.optType, l.investCcy, tenorCap)
	if err != nil {
		return "", err
	}
	var q *pgstore.CexDCIQuote
	for i := range qs {
		if l.strikePass(&qs[i], spot) {
			q = &qs[i]
			break
		}
	}
	if q == nil {
		return "", e.gap(ctx, l, "permission", "dual_quote",
			"%s腿无达标档在售（spot %.0f，strike 界 %s）", l.name, spot, l.boundText(spot))
	}
	now := e.Now()
	reqID := fmt.Sprintf("dci|%s|%s|%s|%.8f", l.name, q.ProductRef, delivDay(q), qty)
	oid, created, err := e.Store.CreateDCIOrder(ctx, &pgstore.DCIOrder{
		RequestID: reqID, Venue: "binance", TemplateID: l.tmpl,
		ProductRef: q.ProductRef, Underlying: "BTC", OptType: l.optType, Strike: q.Strike,
		InvestCcy: q.InvestCcy, Qty: qty, TenorDays: q.TenorDays})
	if err != nil {
		return "", err
	}
	if !created {
		return fmt.Sprintf("%s腿已受理（request_id 防重）", l.name), nil
	}
	body, err := e.post(ctx, q.ProductRef, qty, now)
	if err != nil {
		e.finish(ctx, oid, "failed", err.Error())
		return "", e.gap(ctx, l, "permission", "dual_order",
			"请开通 binance 赚币/双币申购权限（%s腿下单失败：%v）", l.name, err)
	}
	e.finish(ctx, oid, "filled", body)
	strike := 0.0
	if q.Strike != nil {
		strike = *q.Strike
	}
	note := fmt.Sprintf("DCI %s 申购回执：binance %s strike %.0f tenor %dd qty %.6f %s（单号 %s）",
		l.name, q.ProductRef, strike, tenor(q), qty, l.qtyCcy, reqID)
	opened := now
	asset := l.qtyCcy
	if _, err := e.Store.InsertPersonalPosition(ctx, asset, "other", asset+"USDT",
		note, &qty, &opened); err != nil {
		return "", fmt.Errorf("回执入账: %w", err)
	}
	if err := e.Store.InsertAlert(ctx, nil, "info", fmt.Sprintf(
		"DCI %s 申购成交：%.6f %s strike %.0f apy %.4g（单号 %s）",
		l.name, qty, l.qtyCcy, strike, q.APY, reqID)); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s腿申购已提交并入账：%.6f %s strike %.0f（%.4g 原生口径）",
		l.name, qty, l.qtyCcy, strike, q.APY), nil
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
