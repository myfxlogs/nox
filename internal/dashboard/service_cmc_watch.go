// D-234 CMC 看盘实时化：帧钩子通知入口（1s 蓄压）+ 同源现算载荷 + StreamUpdates case 13 订阅辅助。
package dashboard

import (
	"context"
	"time"

	"arb/internal/bus"
	"arb/internal/collect/cmc"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// cmcWatchFlushIntervalDefault D-234：CMC 看盘帧 1s 蓄压间隔（活跃期帧 ≤1s 体现，≤1 条/秒/客户端）。
const cmcWatchFlushIntervalDefault = time.Second

// SetCMCWatchTopic D-234：注入 CMC 看盘实时帧 topic（main 装配；nil=未接线→不订阅）。
func (s *Service) SetCMCWatchTopic(t *bus.Topic[*v1.CMCWatchUpdate]) { s.cmcWatchTopic = t }

// SetCMCWatchFlushInterval D-234：注入蓄压间隔（测试注 10ms 加速；≤0=默认 1s）。
func (s *Service) SetCMCWatchFlushInterval(d time.Duration) { s.cmcWatchFlushInterval = d }

// NotifyCMCFrame D-234：CMC ws 帧钩子通知入口——1s 蓄压（窗口内多帧合并，flush 时现算最后一帧派生值）。
// 线程安全：帧钩子从 cmc ws goroutine 调用，非阻塞（仅设 pending+启定时器）。
// kind 仅作日志/未来分诊用，蓄压合并所有帧类型为一次现算（dashboard 从 DB 取最新态，自然反映全部帧）。
func (s *Service) NotifyCMCFrame(kind cmc.FrameKind) {
	s.cmcFrameMu.Lock()
	defer s.cmcFrameMu.Unlock()
	if s.cmcFrameTimer != nil {
		return // 已有 pending 定时器，本帧并入窗口（合并=只推最后一帧的派生值）
	}
	interval := s.cmcWatchFlushInterval
	if interval <= 0 {
		interval = cmcWatchFlushIntervalDefault
	}
	s.cmcFrameTimer = time.AfterFunc(interval, s.flushCMCWatch)
}

// flushCMCWatch D-234：蓄压窗口到期——现算载荷并 Publish 到 cmcWatchTopic（扇出至所有 StreamUpdates 连接）。
func (s *Service) flushCMCWatch() {
	s.cmcFrameMu.Lock()
	s.cmcFrameTimer = nil
	s.cmcFrameMu.Unlock()
	if s.cmcWatchTopic == nil {
		return
	}
	upd := s.computeCMCWatchUpdate(context.Background())
	s.cmcWatchTopic.Publish(upd)
}

// subscribeCMCWatchTopic D-234：StreamUpdates 订阅辅助（nil topic→nil channel+noop cancel，select 永不命中）。
func (s *Service) subscribeCMCWatchTopic() (<-chan *v1.CMCWatchUpdate, func()) {
	if s.cmcWatchTopic == nil {
		return nil, func() {}
	}
	return s.cmcWatchTopic.Subscribe()
}

// computeCMCWatchUpdate D-234：后端现算 CMC 看盘载荷（与 GetHedgeStructure 同源公式，只取看盘字段不算 carry）。
// mid=LastCMCPriceMidWithTime（REST mid，WS 行 mid=NULL 不污染）；
// ask=LastCMCPriceForSide(Sell)（WS ask 优先回退 REST ask，与 GetHedgeStructure evalAsk 同源）；
// bid=LastCMCPositionByInstrument.EvaluationBidPrice（type7 patch 更新，15s 节流落库）；
// 双腿 PnL=(baseline−pnlPx)×mirrorQty 镜像恒和=0（pnlPx=evalAsk，0 回退 mid）；
// 强平/预警净值=usedMargin×50%/80%（platform_rule_50，D-225 v2.2 标定）。
func (s *Service) computeCMCWatchUpdate(ctx context.Context) *v1.CMCWatchUpdate {
	upd := &v1.CMCWatchUpdate{CloseoutSource: "platform_rule_50"}

	cmcPos, cmcPosErr := s.store.LastCMCPositionByInstrument(ctx, "X-AFQUL")
	hasCMCPos := cmcPosErr == nil
	cmcSum, cmcSumErr := s.store.LastCMCAccountSummary(ctx)
	hasCMCSum := cmcSumErr == nil

	var mid, evalAsk float64
	var quoteFetchedAt time.Time
	if hasCMCPos {
		if v, t, err := s.store.LastCMCPriceMidWithTime(ctx, "X-AFQUL"); err == nil {
			mid = v
			quoteFetchedAt = t
		}
		if px, _, _, err := s.store.LastCMCPriceForSide(ctx, "X-AFQUL", "Sell"); err == nil {
			evalAsk = px
		}
	}

	var baseline float64
	if hasCMCPos {
		baseline = cmcPos.EntryPrice
	}
	// pnlPx=evalAsk（ForSide short=ask 评估价口径），0 回退 mid（与 GetHedgeStructure 同源）
	pnlPx := evalAsk
	if pnlPx == 0 {
		pnlPx = mid
	}

	var mirrorQty float64
	if hasCMCPos {
		mirrorQty = cmcPos.Quantity
		upd.ShortQty = mirrorQty
		upd.Bid = cmcPos.EvaluationBidPrice
		if cmcPos.MarginPercentage > 0 {
			upd.ShortLev = int32(1.0 / cmcPos.MarginPercentage)
		}
		if pnlPx > 0 && baseline > 0 {
			upd.LegShortPnl = (baseline - pnlPx) * mirrorQty
		}
	}
	// 质押腿镜像 pnl（用空单 qty 确保恒和=0；与 GetHedgeStructure 同源）
	if pnlPx > 0 && baseline > 0 && mirrorQty > 0 {
		upd.LegSpotPnl = (pnlPx - baseline) * mirrorQty
	}

	upd.Mid = mid
	upd.Ask = evalAsk
	upd.BaselinePrice = baseline

	if hasCMCSum {
		upd.DistToCloseoutPct = cmcSum.DistanceToCloseoutPct
		upd.CloseoutLevelUsd = cmcSum.TotalEquityUsedInMargin * 0.5
		upd.WarningLevelUsd = cmcSum.TotalEquityUsedInMargin * 0.8
		upd.CmcCash = cmcSum.TotalCashBalance
		upd.CmcEquity = cmcSum.TotalAccountValue
		upd.UsedMargin = cmcSum.TotalEquityUsedInMargin
		upd.PlatformAlertState = cmc.AlertsToState(cmcSum.Alerts)
	}
	if s.cmcWsHealth != nil {
		upd.WsConnected = s.cmcWsHealth()
	}
	if !quoteFetchedAt.IsZero() {
		upd.QuoteTime = timestamppb.New(quoteFetchedAt)
	}
	return upd
}
