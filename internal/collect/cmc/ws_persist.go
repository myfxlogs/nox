// CMC WebSocket 持久化方法（D-225 v2.1，从 ws.go 拆出控行数 <450）。
package cmc

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// extractPriceEvent D-225 v2.1 S2：从持仓内存态提取 X-AFQUL evaluationAskPrice → 价格事件。
func (w *WsClient) extractPriceEvent(ctx context.Context) {
	w.positionsMu.Lock()
	var ask float64
	for i := range w.positions {
		if w.positions[i].InstrumentID == "X-AFQUL" {
			ask = parseFloat(w.positions[i].EvaluationAskPrice)
			break
		}
	}
	w.positionsMu.Unlock()
	if ask <= 0 {
		return
	}
	now := time.Now()
	w.priceMu.Lock()
	w.evalAsk = ask
	w.evalAskTs = now
	prevVal := w.lastPriceVal
	prevTime := w.lastPriceWrite
	shouldWrite := false
	if prevVal <= 0 {
		shouldWrite = true
	} else {
		change := abs(ask-prevVal) / prevVal
		if change > wsPriceMinDelta && now.Sub(prevTime) >= wsPriceThrottle {
			shouldWrite = true
		}
	}
	if shouldWrite {
		w.lastPriceVal = ask
		w.lastPriceWrite = now
	}
	w.priceMu.Unlock()
	if shouldWrite {
		w.persistPrice(ctx, ask, now, false)
	}
}

// persistPrice 写 WS 价格行（src=cmc_ws / 心跳行 cmc_ws_hb——D-370 S4 轻量路径：
// src 区分零 migration，落库间隔统计可 WHERE src 剔除心跳；bid/mid NULL，
// quote_time=到达时刻, market_status=注入值）。
func (w *WsClient) persistPrice(ctx context.Context, ask float64, now time.Time, heartbeat bool) {
	src := "cmc_ws"
	if heartbeat {
		src = "cmc_ws_hb"
	}
	rec := pgstore.CMCPrice{
		InstrumentID: "X-AFQUL",
		AskPrice:     ask,
		MidPrice:     0, // WS 行不合成 mid（NULL）
		BidPrice:     0, // WS 行不合成 bid（NULL）
		MarketStatus: w.marketStatus(),
		QuoteTime:    now, // patch 无时间戳，如实标注=帧到达时刻
		FetchedAt:    now,
		Src:          src,
	}
	if err := w.store.InsertCMCPrice(ctx, rec); err != nil {
		log.Printf("[cmc-ws] insert ws price: %v", err)
		return
	}
	w.emitFact(ctx, []fact.Fact{{
		Kind: fact.KindCMCPrice, Venue: "cmc", Symbol: "TRXUSD",
		Value: ask, Unit: fact.UnitPrice, Ts: now, Src: src,
	}})
}

// persistPriceHeartbeat 4min 心跳行（写最新已知价，保时序连续）。
func (w *WsClient) persistPriceHeartbeat(ctx context.Context) {
	w.priceMu.Lock()
	ask := w.evalAsk
	w.priceMu.Unlock()
	if ask <= 0 {
		return
	}
	now := time.Now()
	w.persistPrice(ctx, ask, now, true) // D-370 S4：heartbeat 行 src=cmc_ws_hb
	// 心跳也更新 lastPriceWrite 防止下一轮非心跳行立即触发
	w.priceMu.Lock()
	w.lastPriceWrite = now
	w.priceMu.Unlock()
}

// recomputeAndFanout D-225 v2.1 S2：closeout 内存重算与落库解耦。
// 每个被接受的事件 → 重算 closeout/distance → 触发 OnEvent（扇出节奏不被落库节流绑架，E5 教训）。
func (w *WsClient) recomputeAndFanout(ctx context.Context) {
	if w.onEvent == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			w.patchWarnCount.Add(1)
			log.Printf("[cmc-ws] onEvent panic: %v", r)
		}
	}()
	w.onEvent(w.LatestSnapshot())
}

// persistSummary D-225 v2.1 S3：type6 节流落库（数据源=type6，有 timestamp）。
func (w *WsClient) persistSummary(ctx context.Context, s *accountSummaryJSON, alerts json.RawMessage) {
	cash := parseFloat(s.TotalCashBalance)
	w.positionsMu.Lock()
	posCopy := make([]positionJSON, len(w.positions))
	copy(posCopy, w.positions)
	w.positionsMu.Unlock()
	// D-225 v2.1：LastCMCPriceMid → LastCMCPriceForSide(short)（ask 保守修正，F11）
	px, _, _, _ := w.store.LastCMCPriceForSide(ctx, "X-AFQUL", "Sell")
	closeout, distance := calcCloseoutValues(cash, &positionsJSON{Positions: posCopy}, px)
	now := time.Now()
	rec := pgstore.CMCAccountSummary{
		AccountID: w.accountID, TotalAccountValue: parseFloat(s.TotalAccountValue),
		TotalCashBalance: cash, TotalUnrealisedPnL: parseFloat(s.TotalUnrealisedPnL),
		TotalEquityFreeForMargin: parseFloat(s.TotalEquityFreeForMargin),
		TotalEquityUsedInMargin:  parseFloat(s.TotalEquityUsedInMargin), // ← totalMarginAmount (F12)
		CloseoutPrice:            closeout, DistanceToCloseoutPct: distance,
		FetchedAt: now, Alerts: alerts,
	}
	if err := w.store.InsertCMCAccountSummary(ctx, rec); err != nil {
		log.Printf("[cmc-ws] insert summary: %v", err)
		return
	}
	w.summaryMu.Lock()
	w.lastSummaryPersist = now
	w.lastType6Val = parseFloat(s.TotalAccountValue)
	w.alertsChanged = false
	w.summaryMu.Unlock()
	w.emitFact(ctx, []fact.Fact{{Kind: fact.KindCMCAccount, Venue: "cmc", Symbol: "summary",
		Value: parseFloat(s.TotalAccountValue), Unit: fact.UnitUSD, Ts: now, Src: "cmc_ws"}})
}

// scheduleFlush D-225 v2.1：持仓落库节流 ≥15s（替换旧 2s burst，F9 实测 2000 行/日 → 目标 ≤400/日）。
// R1 修：无 pending 才 AfterFunc——旧实现每 type7 帧 Stop+重设 15s 定时器，
// patch 流期定时器永不到期 → flushPositions 饿死 → positions 零落库。
func (w *WsClient) scheduleFlush() {
	w.flushMu.Lock()
	defer w.flushMu.Unlock()
	if w.flushTimer != nil {
		return // 已有 pending 定时器，不重设（R1 修）
	}
	w.flushTimer = time.AfterFunc(wsPosFlushInterval, w.flushPositions)
}

func (w *WsClient) flushPositions() {
	// R1 修：清 flushTimer 让后续 scheduleFlush 可设新定时器
	w.flushMu.Lock()
	w.flushTimer = nil
	w.flushMu.Unlock()

	w.positionsMu.Lock()
	posCopy := make([]positionJSON, len(w.positions))
	copy(posCopy, w.positions)
	w.positionsMu.Unlock()
	if len(posCopy) == 0 {
		return
	}
	now := time.Now()
	dbPositions := make([]pgstore.CMCPosition, 0, len(posCopy))
	for _, p := range posCopy {
		dp := pgstore.CMCPosition{
			AccountID: w.accountID, InstrumentID: p.InstrumentID, Direction: p.Direction,
			Quantity: parseFloat(p.Quantity), EntryPrice: parseFloat(p.Price),
			UnrealisedPnL: parseFloat(p.UnrealisedPnLInPrimaryCurrency),
			MarginAmount:  parseFloat(p.MarginAmount), MarginPercentage: parseFloat(p.StandardMarginPercentage),
			EvaluationBidPrice: parseFloat(p.EvaluationBidPrice),
			EvaluationAskPrice: parseFloat(p.EvaluationAskPrice), FetchedAt: now,
		}
		if len(p.Orders) > 0 {
			dp.OrderID = p.Orders[0].OrderID
			if t, err := parseRFC3339Loose(p.Orders[0].SubmittedTimestamp); err == nil {
				dp.OpenedAt = &t
			}
		}
		dbPositions = append(dbPositions, dp)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // D-508 S5
	defer cancel()
	if err := w.store.InsertCMCPositions(ctx, w.accountID, dbPositions); err != nil {
		log.Printf("[cmc-ws] insert positions: %v", err)
		return
	}
	w.emitFact(ctx, []fact.Fact{{Kind: fact.KindCMCAccount, Venue: "cmc",
		Symbol: "positions", Value: 0, Unit: fact.UnitUSD, Ts: now, Src: "cmc_ws"}})
}

func (w *WsClient) emitFact(ctx context.Context, facts []fact.Fact) {
	if w.sink != nil {
		if err := w.sink(ctx, facts); err != nil {
			log.Printf("[cmc-ws] sink: %v", err)
		}
	}
}
