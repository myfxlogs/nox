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

// computeCMCWatchUpdate D-234 R-V → D-236 变更9：后端现算 CMC 看盘载荷（与 GetHedgeStructure 同源 freshLive）。
// D-236 变更9：全字段走 freshLive（内存优先，分域 >30s 回退 DB）；PnL=平台自报值（LiveState.PnL）；
// 镜像恒和=0 退役（业主真值原则取代）；净敞口 USD 分母改流 Mid；净值随 Margin 流值派生。
// bid=LastCMCPositionByInstrument.EvaluationBidPrice（type7 patch 更新，15s 节流落库，DB 兜底）；
// 强平/预警净值=margin×50%/80%（platform_rule_50，D-225 v2.2 标定）。
func (s *Service) computeCMCWatchUpdate(ctx context.Context) *v1.CMCWatchUpdate {
	upd := &v1.CMCWatchUpdate{CloseoutSource: "platform_rule_50"}

	// D-236 变更9：全字段走 freshLive（与 GetHedgeStructure 同源）
	ls := s.freshLive(ctx)

	cmcPos, cmcPosErr := s.store.LastCMCPositionByInstrument(ctx, "X-AFQUL")
	hasCMCPos := cmcPosErr == nil
	cmcSum, cmcSumErr := s.store.LastCMCAccountSummary(ctx)
	hasCMCSum := cmcSumErr == nil

	var baseline float64
	if hasCMCPos {
		baseline = cmcPos.EntryPrice
	}

	// 空单腿：D-236 变更9 PnL=平台自报值（LiveState.PnL）
	if ls.Qty > 0 {
		upd.ShortQty = ls.Qty
	}
	if hasCMCPos {
		upd.Bid = cmcPos.EvaluationBidPrice
	}
	if ls.Lev > 0 {
		upd.ShortLev = int32(ls.Lev)
	}
	upd.LegShortPnl = ls.PnL // 平台真值（业主令：取 cmc 真实数据，不再反推计算）
	// 质押腿 PnL 不再镜像计算（现货无对手方报价、无开仓成本记录，反推=虚构）→ 前端显示「—」

	upd.Mid = ls.Mid
	upd.Ask = ls.Ask
	upd.BaselinePrice = baseline

	// D-236 变更9：净值/距强平随 Margin 流值派生（D-225 口径不变：margin×50%/80%）
	margin := ls.Margin
	if margin <= 0 && hasCMCSum {
		margin = cmcSum.TotalEquityUsedInMargin // DB 兜底
	}
	if hasCMCSum {
		upd.DistToCloseoutPct = cmcSum.DistanceToCloseoutPct
		upd.ShortLiq = cmcSum.CloseoutPrice
		upd.PlatformAlertState = cmc.AlertsToState(cmcSum.Alerts)
	}
	upd.CloseoutLevelUsd = margin * 0.5
	upd.WarningLevelUsd = margin * 0.8
	upd.CmcCash = ls.Cash
	upd.CmcEquity = ls.Equity
	upd.UsedMargin = margin
	// 净敞口 USD（与 GetHedgeStructure 同源：netExposureTrx × 流 Mid）
	if ls.Qty > 0 {
		trxAcc, trxErr := s.store.LastTRXAccount(ctx)
		if trxErr == nil {
			balance := float64(trxAcc.BalanceSun) / 1e6
			staked := float64(trxAcc.VotesTotal)
			netExposureTrx := balance + staked - ls.Qty
			upd.NetExposureUsd = netExposureTrx * ls.Mid
		}
	}
	if s.cmcWsHealth != nil {
		upd.WsConnected = s.cmcWsHealth()
	}
	// quote_time 用 PosTs（流价格帧时戳，与 GetHedgeStructure 同源）
	if !ls.PosTs.IsZero() {
		upd.QuoteTime = timestamppb.New(ls.PosTs)
	}
	return upd
}

// freshLive D-236 变更9：内存 LiveState 优先（cmcLiveProvider），分域新鲜度 >30s 回退 DB。
// position 域（Ask/Mid/Qty/Lev/PnL）：PosTs 陈旧/未接线→回退 DB positions 行；
// summary 域（Cash/Equity/Margin）：SumTs 陈旧/未接线→回退 DB summary 行。
// 替代 D-234 freshEvalAsk（单解析器统一 position+summary 域，自审 B 项违规修正）。
func (s *Service) freshLive(ctx context.Context) cmc.LiveState {
	var ls cmc.LiveState
	if s.cmcLiveProvider != nil {
		ls = s.cmcLiveProvider()
	}
	// position 域回退：PosTs 陈旧/未接线/Ask=0 → 读 DB positions 行
	posFresh := !ls.PosTs.IsZero() && time.Since(ls.PosTs) <= 30*time.Second
	if !posFresh || ls.Ask <= 0 {
		cmcPos, err := s.store.LastCMCPositionByInstrument(ctx, "X-AFQUL")
		if err == nil {
			if !posFresh || ls.Ask <= 0 {
				ls.Ask = cmcPos.EvaluationAskPrice
			}
			if !posFresh || ls.Mid <= 0 {
				ls.Mid = cmcPos.EvaluationBidPrice // DB 回退用 bid（无独立 mid 字段；流 mid=(ask+bid)/2 优先）
			}
			if !posFresh || ls.Qty <= 0 {
				ls.Qty = cmcPos.Quantity
			}
			if !posFresh || ls.Lev <= 0 {
				if cmcPos.MarginPercentage > 0 {
					ls.Lev = 1.0 / cmcPos.MarginPercentage
				}
			}
			if !posFresh || ls.PnL == 0 {
				ls.PnL = cmcPos.UnrealisedPnL
			}
			if !posFresh {
				ls.PosTs = cmcPos.FetchedAt
			}
		}
	}
	// summary 域回退：SumTs 陈旧/未接线 → 读 DB summary 行
	sumFresh := !ls.SumTs.IsZero() && time.Since(ls.SumTs) <= 30*time.Second
	if !sumFresh {
		cmcSum, err := s.store.LastCMCAccountSummary(ctx)
		if err == nil {
			ls.Cash = cmcSum.TotalCashBalance
			ls.Equity = cmcSum.TotalAccountValue
			ls.Margin = cmcSum.TotalEquityUsedInMargin
			ls.SumTs = cmcSum.FetchedAt
		}
	}
	return ls
}
