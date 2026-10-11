// exec.go —— D-191：DCI 申购执行轨（covered-call 首用例，binance SAPI 写径）。
// 触发=业主「go」口令（飞书拦截/RPC 同径）；有界硬线=份额≤custody_share 约束+
// 期限≤14d（custody_redeemable 消费）+strike 档取报价面最优档；
// 幂等=fin_dci_order.request_id UNIQUE 单次尝试不重试；失败→fin_condition_gap 请求。
package dciexec

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"arb/internal/store/pgstore"
)

// Store 执行面依赖（*pgstore.Store 实现；测试走 fake）。
type Store interface {
	ListCapabilityConstraints(ctx context.Context) ([]pgstore.CapabilityConstraint, error)
	ListCapabilityLedger(ctx context.Context) (*pgstore.CapabilityLedger, error)
	BestDCIQuote(ctx context.Context, venue, underlying, optType string, maxTenorDays int) (*pgstore.CexDCIQuote, error)
	CreateDCIOrder(ctx context.Context, o *pgstore.DCIOrder) (int64, bool, error)
	FinishDCIOrder(ctx context.Context, id int64, status, response string) error
	InsertPersonalPosition(ctx context.Context, asset, kind, priceSymbol, note string, qty *float64, openedAt *time.Time) (int64, error)
	OpenConditionGap(ctx context.Context, g *pgstore.ConditionGap) (int64, error)
	RequestConditionGap(ctx context.Context, id int64) error
	InsertAlert(ctx context.Context, ruleID *int64, level, message string) error
}

// Executor binance DCI 申购执行器（凭据由装配侧从 exchange_configs 注入）。
type Executor struct {
	Store  Store
	BnKey  string
	BnSec  string
	Base   string // sapi base（代理/直连由装配决议）
	Client *http.Client
	Now    func() time.Time // 测试可覆
}

// sign HMAC-SHA256 hex（binance sapi 同法，包内独立实现 P3 不适用）。
func sign(secret, query string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(query))
	return hex.EncodeToString(mac.Sum(nil))
}

// SubscribeBody 申购参数构造（projectId+lots+时间戳；签名=HMAC(query)）。
func (e *Executor) SubscribeBody(projectID string, lots float64, at time.Time) (query, url string) {
	q := fmt.Sprintf("projectId=%s&lots=%.8f&timestamp=%d&recvWindow=5000",
		projectID, lots, at.UnixMilli())
	return q, fmt.Sprintf("%s/sapi/v1/dci/product/subscribe?%s&signature=%s",
		strings.TrimRight(e.Base, "/"), q, sign(e.BnSec, q))
}

// Go covered-call 申购主径（业主口令「go」→本径；约束/报价/幂等三段闸）。
func (e *Executor) Go(ctx context.Context) (string, error) {
	if e.BnKey == "" {
		return "", e.gap(ctx, "请配置 binance 凭据（DCI 申购写径）", "credential", "binance:spot")
	}
	// ① 约束界：custody_share 必在（缺失=约束未喂→缺口），redeemable→14d 上限。
	var share *float64
	var redeem bool
	cons, err := e.Store.ListCapabilityConstraints(ctx)
	if err != nil {
		return "", err
	}
	for _, c := range cons {
		if c.Subject == "custody_share" {
			share = c.ValueNum
		}
		if c.Subject == "custody_redeemable" {
			redeem = true
		}
	}
	if share == nil {
		return "", e.gap(ctx, "请喂约束 custody_share（托管可动份额上限）", "constraint", "custody_share")
	}
	tenorCap := 30
	if redeem { // 随时赎约束→期限硬线 14d（票面钦定）
		tenorCap = 14
	}
	// ② 报价档：binance BTC 卖高（opt_type=C）、≤tenorCap、apy 最优。
	q, err := e.Store.BestDCIQuote(ctx, "binance", "BTC", "C", tenorCap)
	if err != nil || q == nil {
		return "", e.gap(ctx, "binance BTC 卖高报价缺（tenor≤%dd）", "permission", "dual_quote", tenorCap)
	}
	// ③ 数量：BTC 持仓×份额上限（有界硬线）。
	led, err := e.Store.ListCapabilityLedger(ctx)
	if err != nil {
		return "", err
	}
	var held float64
	for _, h := range led.Holdings {
		if h.Asset == "BTC" {
			held += h.Qty
		}
	}
	qty := held * *share
	if qty <= 0 {
		return "", e.gap(ctx, "BTC 托管可动份额=0（持仓 %.4f×share %.2f）", "holding", "BTC", held, *share)
	}
	// ④ 幂等登记（request_id 防重：同 product×delivery×qty 不重发）。
	now := e.Now()
	reqID := fmt.Sprintf("dci|%s|%s|%.8f", q.ProductRef,
		delivDay(q), qty)
	oid, created, err := e.Store.CreateDCIOrder(ctx, &pgstore.DCIOrder{
		RequestID: reqID, Venue: "binance", TemplateID: "dci_sell_high_covered_call",
		ProductRef: q.ProductRef, Underlying: "BTC", OptType: "C", Strike: q.Strike,
		InvestCcy: q.InvestCcy, Qty: qty, TenorDays: q.TenorDays})
	if err != nil {
		return "", err
	}
	if !created {
		return "DCI 申购已受理（request_id 防重，无重发）", nil
	}
	// ⑤ 签名 POST（单次尝试不重试；败→failed+gap 请求）。
	body, err := e.post(ctx, q.ProductRef, qty, now)
	if err != nil {
		e.finish(ctx, oid, "failed", err.Error())
		return "", e.gap(ctx, "请开通 binance 赚币/双币申购权限（下单失败：%v）", "permission", "dual_order")
	}
	e.finish(ctx, oid, "filled", body)
	// ⑥ 回执入账：personal_positions 行+income 锚 note。
	posQty := qty
	strike := 0.0
	if q.Strike != nil {
		strike = *q.Strike
	}
	note := fmt.Sprintf("DCI covered-call 申购回执：binance %s strike %.0f tenor %dd qty %.6f（单号 %s）",
		q.ProductRef, strike, tenor(q), qty, reqID)
	opened := now
	if _, err := e.Store.InsertPersonalPosition(ctx, "BTC", "other", "BTCUSDT",
		note, &posQty, &opened); err != nil {
		return "", fmt.Errorf("回执入账: %w", err)
	}
	if err := e.Store.InsertAlert(ctx, nil, "info",
		fmt.Sprintf("DCI 申购成交：BTC %.6f strike %.0f apy %.4g（单号 %s）", qty, strike, q.APY, reqID)); err != nil {
		return "", err
	}
	return fmt.Sprintf("申购已提交并回执入账：BTC %.6f strike %.0f（%.4g 原生口径）", qty, strike, q.APY), nil
}

func (e *Executor) post(ctx context.Context, projectID string, lots float64, at time.Time) (string, error) {
	_, url := e.SubscribeBody(projectID, lots, at)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-MBX-APIKEY", e.BnKey)
	resp, err := e.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("subscribe HTTP %d: %s", resp.StatusCode, string(b))
	}
	return string(b), nil
}

func (e *Executor) client() *http.Client {
	if e.Client != nil {
		return e.Client
	}
	return http.DefaultClient
}

func (e *Executor) finish(ctx context.Context, id int64, st, resp string) {
	if err := e.Store.FinishDCIOrder(ctx, id, st, resp); err != nil {
		log.Printf("[dciexec] finish order %d: %v", id, err)
	}
}

// gap 缺口落+请求（D-186 通道复用；err 回执即缺口文案）。
func (e *Executor) gap(ctx context.Context, format, kind, subject string, args ...any) error {
	text := fmt.Sprintf(format, args...)
	id, err := e.Store.OpenConditionGap(ctx, &pgstore.ConditionGap{
		TemplateID: "dci_sell_high_covered_call", Kind: kind, Subject: subject,
		RequestText: text})
	if err != nil {
		return err
	}
	if err := e.Store.RequestConditionGap(ctx, id); err != nil {
		return err
	}
	return fmt.Errorf("条件缺口：%s", text)
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
