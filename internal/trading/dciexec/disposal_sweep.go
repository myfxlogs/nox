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
		// D-651 A 段：venue 先锚后卖——处置卖出按所得所在 venue 路由
		// （原钉 binance=okx 所得不可达病灶）。锚缺行保 binance 旧径
		// （10-15 事故遗留行全 binance 域）。锚读败=本轮留行非静默卖错所。
		ordID := int64(0)
		if m := pendingDisposalOrderRE.FindStringSubmatch(p.Note); m != nil {
			ordID, _ = strconv.ParseInt(m[1], 10, 64)
		}
		invest, uly := 0.0, ccy+"USDT"
		anchored := false
		venue := "binance"
		if ordID > 0 {
			if o, lerr := e.Store.DCIOrderByID(ctx, ordID); lerr != nil {
				log.Printf("[dci-sweep] order %d anchor: %v", ordID, lerr)
			} else if o != nil {
				invest, uly, anchored = o.Qty, o.Underlying, true
				if o.Venue != "" {
					venue = o.Venue
				}
			}
		}
		proceeds, err := e.venueSpotSell(ctx, venue, ccy, qty)
		if err != nil {
			msg := fmt.Sprintf("pending_disposal 行#%d 接货 %.8f %s@%s 卖回仍败: %v", p.ID, qty, ccy, venue, err)
			if aerr := e.Store.InsertAlertDedup(ctx, "warn", msg, 24*time.Hour); aerr != nil {
				log.Printf("[dci-sweep] alert: %v", aerr)
			}
			continue
		}
		// 损益锚：note 内 DCI#id → 原单单点读（DCIOrderByID——ListDCIOrders
		// 有 >200→clamp20 静默截断，10-15 实证 id=11 被截致 invest=0、得额
		// 351.94 被当净利 +351.94 虚增入账）。锚缺→不记 income（不编差值——
		// 得额≠损益），行仍照关（卖回是真账）+ 告警明示人工补记。
		pnl := proceeds - invest
		if anchored {
			cfgID, cerr := e.Store.ExchangeConfigID(ctx, venue, "spot")
			if cerr != nil || cfgID == 0 {
				log.Printf("[dci-sweep] order %d exchange_config 跳收益账（fail-open）cerr=%v cfgID=%d", ordID, cerr, cfgID)
			}
			if cerr == nil && cfgID > 0 {
				if _, ierr := e.Store.InsertIncomeEvents(ctx, []pgstore.IncomeEvent{{
					Venue: venue, VenueType: "spot", ConfigID: cfgID,
					Symbol:     uly,
					IncomeType: "dci_exercise_disposal",
					AmountUSD:  pnl, TranID: ordID*10 + 7, Ts: e.Now()}}); ierr != nil {
					log.Printf("[dci-sweep] income: %v", ierr)
				}
			}
		}
		marker := "pending_disposal"
		if ordID > 0 {
			marker = fmt.Sprintf("DCI#%d", ordID)
		}
		pnlNote := fmt.Sprintf("净损益 %+.2f", pnl)
		if !anchored {
			pnlNote = "原单锚缺——损益未入账待人工补记"
		}
		if _, cerr := e.Store.ClosePersonalPositionsByMarker(ctx, marker,
			fmt.Sprintf("已清扫卖回得 %.2f USDT（%s）", proceeds, pnlNote)); cerr != nil {
			log.Printf("[dci-sweep] close row %d: %v", p.ID, cerr)
		}
		if aerr := e.Store.InsertAlert(ctx, nil, "warn", fmt.Sprintf(
			"pending_disposal 清扫：%.8f %s 已市价卖回得 %.2f USDT（%s，DCI#%d）",
			qty, ccy, proceeds, pnlNote, ordID)); aerr != nil {
			log.Printf("[dci-sweep] alert: %v", aerr)
		}
		done++
	}
	return done, nil
}

// venueSpotSell D-651 A 段：处置卖出按 venue 路由（所得所在所卖回——binance=
// 现货市价卖旧径；okx=okxMarketSell rail 件；无 venue 卖径=如实错留行）。
func (e *Executor) venueSpotSell(ctx context.Context, venue, ccy string, qty float64) (float64, error) {
	if e.SpotSellFn != nil { // 测试注口语义钉 binance 径旧约（venue 形参不消费）
		return e.SpotSellFn(ctx, ccy+"USDT", qty)
	}
	switch venue {
	case "binance":
		if e.BnKey == "" {
			return 0, fmt.Errorf("binance 凭据缺=无卖径")
		}
		return e.spotMarketSell(ctx, ccy+"USDT", qty)
	case "okx":
		r, ok := e.earnRail("okx")
		if !ok {
			return 0, fmt.Errorf("okx rail 凭据缺=无卖径")
		}
		or, cast := r.(*okxEarnRail)
		if !cast {
			return 0, fmt.Errorf("okx rail 替身非真件（测试注 EarnRails 无法强转）")
		}
		return okxMarketSell(ctx, or, ccy, qty)
	}
	return 0, fmt.Errorf("%s 现货卖径未接（venue 卖面缺口）", venue)
}
