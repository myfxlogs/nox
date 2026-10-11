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

// sign HMAC-SHA256 hex（binance sapi 同法，包内独立实现 P3 不适用）。
func sign(secret, query string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(query))
	return hex.EncodeToString(mac.Sum(nil))
}

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
	BalanceFn func(ctx context.Context, asset string) (float64, error) // 现货 free+locked
	EarnFn    func(ctx context.Context, asset string) ([]EarnPosition, error)
	RedeemFn  func(ctx context.Context, productID string, amount float64) error
	StreamFn  func() bool // 流存活闸（nil=dephealth 实读）
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

// poolBalance 托管池余额=现货(free+locked)+赚币子账持仓（R6 池口径；
// outstanding DCI 锁仓在对账侧回填，下单 qty 不含——在外单不再复投）。
func (e *Executor) poolBalance(ctx context.Context, asset string) (float64, error) {
	bal, err := e.freeBalance(ctx, asset)
	if err != nil {
		return 0, err
	}
	eps, err := e.earnPositionsOf(ctx, asset)
	if err != nil {
		return 0, err
	}
	for _, p := range eps {
		bal += p.Amount
	}
	return bal, nil
}

func (e *Executor) earnPositionsOf(ctx context.Context, asset string) ([]EarnPosition, error) {
	if e.EarnFn != nil {
		return e.EarnFn(ctx, asset)
	}
	return e.earnPositions(ctx, asset)
}

func (e *Executor) redeem(ctx context.Context, productID string, amount float64) error {
	if e.RedeemFn != nil {
		return e.RedeemFn(ctx, productID, amount)
	}
	return e.redeemFlexible(ctx, productID, amount)
}

// redeemShortfall 活期赎回缺口：逐 flexible 持仓赎至凑齐 need（locked 定期不赎——
// 强制赎回罚息面禁动）；返回实赎量。
func (e *Executor) redeemShortfall(ctx context.Context, asset string, need float64) (float64, error) {
	eps, err := e.earnPositionsOf(ctx, asset)
	if err != nil {
		return 0, err
	}
	var done float64
	for _, p := range eps {
		if p.Locked || need-done <= 0 {
			continue
		}
		amt := p.Amount
		if amt > need-done {
			amt = need - done
		}
		if err := e.redeem(ctx, p.ProductID, amt); err != nil {
			return done, err
		}
		done += amt
	}
	return done, nil
}

// tryLeg 单腿：池余额×share→qty、spot 不足→活期赎回缺口→重读、候选档 strike 闸
// →最优、幂等→签名 POST→回执（R6 前置 redeem 步）。
func (e *Executor) tryLeg(ctx context.Context, l leg, share float64, tenorCap int, spot float64) (string, error) {
	bal, err := e.poolBalance(ctx, l.qtyCcy)
	if err != nil {
		return "", e.gap(ctx, l, "permission", "earn_position",
			"请开通 binance 赚币子账查询权限（%s 池余额读败：%v）", l.qtyCcy, err)
	}
	qty := bal * share
	if qty <= 0 {
		return fmt.Sprintf("%s腿跳过（%s 可动=0）", l.name, l.qtyCcy), nil
	}
	// 前置赎回步：现货可动不足→赚币活期赎回缺口→重读现货（单次步零重试）。
	spotFree, err := e.freeBalance(ctx, l.qtyCcy)
	if err != nil {
		return "", fmt.Errorf("%s 现货余额读败：%w", l.qtyCcy, err)
	}
	if spotFree < qty {
		redeemed, rerr := e.redeemShortfall(ctx, l.qtyCcy, qty-spotFree)
		if rerr != nil {
			return "", e.gap(ctx, l, "permission", "earn_redeem",
				"请开通 binance 赚币活期赎回权限（%s 赎回败：%v）", l.qtyCcy, rerr)
		}
		if redeemed > 0 {
			if spotFree, err = e.freeBalance(ctx, l.qtyCcy); err != nil {
				return "", fmt.Errorf("%s 赎回后现货重读败：%w", l.qtyCcy, err)
			}
		}
		if spotFree < qty {
			return "", e.gap(ctx, l, "holding", l.qtyCcy,
				"%s 现货可动不足（池算 qty %.6f、赎回后现货 %.6f）", l.qtyCcy, qty, spotFree)
		}
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
