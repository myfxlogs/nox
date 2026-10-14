// disposal.go —— D-517 DCI 接货自动卖回：PUT 行权落标的币（BTC）→现货市价
// 卖回 USDT 即时结清（事故仓不过夜——不挂限价等回调，wheel 另议）。
// 钩子挂 bookSettlement 尾段（CAS 已翻 settled 幂等闸——处置只跑一次）：
// 卖回得额→income_events `dci_exercise_disposal` 记净损益（卖回得额−申购
// 付额，亏如实负值）；卖败有界重试→warn 告警+personal_positions 留仓标
// pending_disposal（venue 权限/余额缺不臆造——如实留仓等人工）。
package dciexec

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"nox/internal/store/pgstore"
)

// disposalMaxRetry 卖回有界重试（网络/5xx 类瞬时面；venue 拒单不重症）。
const disposalMaxRetry = 3

// disposeExercise 行权接货处置：跨币到账（settleCcy≠investCcy 且非 USD 族）→
// 市价卖回。USD 族到账=未行权/同币终态不处置。
func (e *Executor) disposeExercise(ctx context.Context, o pgstore.FilledDCIOrder, ccy string, qty float64) {
	if qty <= 0 || ccy == "" || strings.EqualFold(ccy, o.InvestCcy) {
		return
	}
	switch strings.ToUpper(ccy) {
	case "USD", "USDT", "USDC", "BUSD", "FDUSD":
		return // USD 族到账非接货
	}
	if e.BnKey == "" {
		e.markPendingDisposal(ctx, o, ccy, qty, "binance 凭据缺——现货卖回无径")
		return
	}
	symbol := strings.ToUpper(ccy) + "USDT"
	var lastErr error
	for i := 0; i < disposalMaxRetry; i++ {
		proceeds, err := e.spotMarketSell(ctx, symbol, qty)
		if err == nil {
			// 损益=卖回得额−申购付额（USDT 同币直接差，亏如实负值落账）。
			pnl := proceeds - o.Qty
			cfgID, cerr := e.Store.ExchangeConfigID(ctx, "binance", "spot")
			if cerr == nil && cfgID > 0 {
				if n, ierr := e.Store.InsertIncomeEvents(ctx, []pgstore.IncomeEvent{{
					Venue: "binance", VenueType: "spot", ConfigID: cfgID,
					Symbol:     o.Underlying,
					IncomeType: "dci_exercise_disposal",
					AmountUSD:  pnl, TranID: o.ID*10 + 3, Ts: e.Now()}}); ierr != nil {
					log.Printf("[dci-dispose] order %d disposal income: %v", o.ID, ierr)
				} else if n > 0 {
					log.Printf("[dci-dispose] order %d disposal pnl %.4f 入账", o.ID, pnl)
				}
			}
			if aerr := e.Store.InsertAlert(ctx, nil, "warn", fmt.Sprintf(
				"DCI 订单#%d 接货 %.8f %s 已市价卖回：得 %.2f USDT，净损益 %+.2f（D-517）",
				o.ID, qty, ccy, proceeds, pnl)); aerr != nil {
				log.Printf("[dci-dispose] order %d alert: %v", o.ID, aerr)
			}
			return
		}
		lastErr = err
		if ctx.Err() != nil {
			break // ctx 取消=中止重试走留仓标
		}
		time.Sleep(2 * time.Second)
	}
	e.markPendingDisposal(ctx, o, ccy, qty,
		fmt.Sprintf("市价卖回 %d 次尽败：%v", disposalMaxRetry, lastErr))
}

// markPendingDisposal 留仓标：personal_positions 记 pending_disposal 行（qty=
// 接货量）+warn 告警——venue 权限/余额缺如实留仓不臆造，人工可寻迹处置。
func (e *Executor) markPendingDisposal(ctx context.Context, o pgstore.FilledDCIOrder, ccy string, qty float64, why string) {
	now := e.Now()
	if _, err := e.Store.InsertPersonalPosition(ctx, ccy, "other", "USDT",
		fmt.Sprintf("pending_disposal D-517：DCI#%d 行权接货待卖回（%s）", o.ID, why),
		&qty, &now); err != nil {
		log.Printf("[dci-dispose] order %d pending_disposal 落行: %v", o.ID, err)
	}
	if aerr := e.Store.InsertAlert(ctx, nil, "warn", fmt.Sprintf(
		"DCI 订单#%d 接货 %.8f %s 卖回失败——pending_disposal 留仓（%s）",
		o.ID, qty, ccy, why)); aerr != nil {
		log.Printf("[dci-dispose] order %d pending alert: %v", o.ID, aerr)
	}
}

// spotMarketSell binance 现货市价卖（POST /v3/order MARKET SELL quantity）。
// 返回 cummulativeQuoteQty=USDT 得额；测试注 SpotSellFn。
func (e *Executor) spotMarketSell(ctx context.Context, symbol string, qty float64) (float64, error) {
	if e.SpotSellFn != nil {
		return e.SpotSellFn(ctx, symbol, qty)
	}
	q := fmt.Sprintf("symbol=%s&side=SELL&type=MARKET&quantity=%.8f&timestamp=%d&recvWindow=5000",
		symbol, qty, e.Now().UnixMilli())
	url := fmt.Sprintf("%s/v3/order?%s&signature=%s",
		strings.TrimRight(e.APIBase, "/"), q, sign(e.BnSec, q))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("X-MBX-APIKEY", e.BnKey)
	resp, err := e.client().Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 != 2 {
		return 0, fmt.Errorf("spot sell HTTP %d: %s", resp.StatusCode, string(b))
	}
	var r struct {
		Status              string `json:"status"`
		CummulativeQuoteQty string `json:"cummulativeQuoteQty"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return 0, fmt.Errorf("spot sell decode: %w", err)
	}
	if r.Status != "FILLED" {
		return 0, fmt.Errorf("spot sell status=%s（非 FILLED——%s）", r.Status, string(b))
	}
	return strconv.ParseFloat(r.CummulativeQuoteQty, 64)
}
