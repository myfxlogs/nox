// disposal_sweep.go —— pending_disposal 周期清扫（10-15 事故次生缺口：卖败留仓行
// 落库后无件再碰=永等人工）。挂 cex_scan 拍搭车（零新定时器）：OPEN 且
// note 含 'pending_disposal' 的行→重试市价卖回→成功则损益入账+关行+warn；
// 仍败保留行、告警 24h 去重不刷屏。
package dciexec

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"nox/internal/store/pgstore"
)

// pendingDisposalOrderRE pending_disposal note 内 DCI 单号锚（「DCI#11」）。
var pendingDisposalOrderRE = regexp.MustCompile(`DCI#(\d+)`)

// SweepPendingDisposals 清扫留仓行（cex_scan 拍驱动）：重试卖回——成功关行+
// 损益入账+warn 回执；败则留行+24h 去重 warn。返回本轮成功处置数。
func (e *Executor) SweepPendingDisposals(ctx context.Context) (int, error) {
	rows, err := e.Store.ListOpenPersonalPositions(ctx)
	if err != nil {
		return 0, fmt.Errorf("pending_disposal 扫面: %w", err)
	}
	done := 0
	for _, p := range rows {
		if p.Kind != "other" || !strings.Contains(p.Note, "pending_disposal") || p.Qty == nil || *p.Qty <= 0 {
			continue
		}
		qty, ccy := *p.Qty, strings.ToUpper(p.Asset)
		if e.BnKey == "" {
			continue // 凭据缺=无卖径，静默留行（首败已告警）
		}
		proceeds, err := e.spotMarketSell(ctx, ccy+"USDT", qty)
		if err != nil {
			msg := fmt.Sprintf("pending_disposal 行#%d 接货 %.8f %s 卖回仍败: %v", p.ID, qty, ccy, err)
			if aerr := e.Store.InsertAlertDedup(ctx, "warn", msg, 24*time.Hour); aerr != nil {
				log.Printf("[dci-sweep] alert: %v", aerr)
			}
			continue
		}
		// 损益锚：note 内 DCI#id → 原单 qty=申购付额；查无则只记得额（不编差值）。
		ordID := int64(0)
		if m := pendingDisposalOrderRE.FindStringSubmatch(p.Note); m != nil {
			ordID, _ = strconv.ParseInt(m[1], 10, 64)
		}
		invest, uly := 0.0, ccy+"USDT"
		if ordID > 0 {
			if orders, lerr := e.Store.ListDCIOrders(ctx, 500); lerr == nil {
				for _, o := range orders {
					if o.ID == ordID {
						invest, uly = o.Qty, o.Underlying
						break
					}
				}
			}
		}
		pnl := proceeds - invest
		if cfgID, cerr := e.Store.ExchangeConfigID(ctx, "binance", "spot"); cerr == nil && cfgID > 0 {
			if _, ierr := e.Store.InsertIncomeEvents(ctx, []pgstore.IncomeEvent{{
				Venue: "binance", VenueType: "spot", ConfigID: cfgID,
				Symbol:     uly,
				IncomeType: "dci_exercise_disposal",
				AmountUSD:  pnl, TranID: ordID*10 + 7, Ts: e.Now()}}); ierr != nil {
				log.Printf("[dci-sweep] income: %v", ierr)
			}
		}
		marker := "pending_disposal"
		if ordID > 0 {
			marker = fmt.Sprintf("DCI#%d", ordID)
		}
		if _, cerr := e.Store.ClosePersonalPositionsByMarker(ctx, marker,
			fmt.Sprintf("已清扫卖回得 %.2f USDT（净损益 %+.2f）", proceeds, pnl)); cerr != nil {
			log.Printf("[dci-sweep] close row %d: %v", p.ID, cerr)
		}
		if aerr := e.Store.InsertAlert(ctx, nil, "warn", fmt.Sprintf(
			"pending_disposal 清扫：%.8f %s 已市价卖回得 %.2f USDT（净损益 %+.2f，DCI#%d）",
			qty, ccy, proceeds, pnl, ordID)); aerr != nil {
			log.Printf("[dci-sweep] alert: %v", aerr)
		}
		done++
	}
	return done, nil
}
