// CMC WebSocket 事件处理器（D-225 v2.1，从 ws.go 拆出控行数 <450）。
package cmc

import (
	"context"
	"encoding/json"
	"log"
	"strconv"
	"strings"
	"time"
)

// handleType5 D-225 v2.1：降为纯交叉校验流（不落库、不供数）。
// 与 type6 关键字段漂移 >0.5% 持续 3 窗 → log warn（schema 漂移探针）。
func (w *WsClient) handleType5(ctx context.Context, data json.RawMessage) {
	var s accountSummaryJSON
	if err := json.Unmarshal(data, &s); err != nil {
		return
	}
	v5 := parseFloat(s.TotalAccountValue)
	w.summaryMu.Lock()
	v6 := w.lastType6Val
	w.summaryMu.Unlock()
	if v6 == 0 {
		return // type6 尚未到达，跳过
	}
	drift := abs(v5-v6) / v6
	w.crossCheckMu.Lock()
	w.lastType5Val = v5
	if drift > wsCrossCheckDrift {
		w.crossCheckStrikes++
		if w.crossCheckStrikes >= wsCrossCheckStrikes {
			log.Printf("[cmc-ws] cross-check drift: type5 totalAccountValue=%v vs type6=%v, drift=%.4f%%, %d strikes",
				v5, v6, drift*100, w.crossCheckStrikes)
			w.crossCheckStrikes = 0 // 告警后重置
		}
	} else {
		w.crossCheckStrikes = 0
	}
	w.crossCheckMu.Unlock()
}

// handleType6 D-225 v2.1：账户主数据源（全量自持，F2）。
// 解析 type6 全量 payload → 内存态更新 → closeout 重算 → 扇出；落库节流（S3）。
func (w *WsClient) handleType6(ctx context.Context, data json.RawMessage) {
	var v type6JSON
	if err := json.Unmarshal(data, &v); err != nil {
		return
	}
	// alerts 变化检测（F13）
	alerts := v.AccountValueAlerts
	if len(alerts) == 0 {
		alerts = json.RawMessage("[]")
	}
	w.summaryMu.Lock()
	alertsChanged := !jsonEqual(w.lastAlerts, alerts)
	w.lastAlerts = alerts
	w.alertsChanged = w.alertsChanged || alertsChanged
	// 全量自持：从 type6 取所有字段
	s := accountSummaryJSON{
		PrimaryAccountCurrency:   v.PrimaryAccountCurrency,
		TotalAccountValue:        v.TotalAccountValue,
		TotalEquityFreeForMargin: v.TotalEquityFreeForMargin,
		TotalEquityUsedInMargin:  v.TotalMarginAmount, // F12: totalMarginAmount ≡ usedInMargin
		TotalCashBalance:         v.TotalCashBalance,
		TotalUnrealisedPnL:       v.TotalUnrealisedPnL,
	}
	w.lastSummary = s
	// quote 时间 ← payload timestamp
	if v.Timestamp != "" {
		if t, err := parseRFC3339Loose(v.Timestamp); err == nil {
			w.lastSummaryTs = t
		}
	}
	delta := parseFloat(v.TotalAccountValue) - w.lastType6Val
	shouldPersist := time.Since(w.lastSummaryPersist) >= wsSummaryThrottle &&
		(delta > wsSummaryMinDelta || delta < -wsSummaryMinDelta)
	forceFlush := time.Since(w.lastSummaryPersist) >= wsSummaryForceFlush
	w.summaryMu.Unlock()

	if shouldPersist || forceFlush {
		w.persistSummary(ctx, &s, alerts)
	}
	if alertsChanged {
		log.Printf("[cmc-ws] alerts changed: %s", string(alerts))
	}
	w.recomputeAndFanout(ctx)
}

// handleType7 D-225 v2.1：持仓+价格主数据源（F3/F4）。
// 首帧全量快照=种子（取消 REST 播种）；patch 驱动持仓内存态+价格事件。
func (w *WsClient) handleType7(ctx context.Context, msg *wsMessage) {
	// 首帧全量快照（data 非 null）→ 种子
	if msg.Data != nil && len(msg.Data) > 0 && string(msg.Data) != "null" {
		var snap positionsJSON
		if err := json.Unmarshal(msg.Data, &snap); err == nil && len(snap.Positions) > 0 {
			w.positionsMu.Lock()
			w.positions = snap.Positions
			w.positionsMu.Unlock()
			w.seedDone.Store(true)
			w.scheduleFlush()
			w.extractPriceEvent(ctx)
			w.recomputeAndFanout(ctx)
			return
		}
	}
	// patch 分发：收集需 REST 重播种的 patch（避免在 positionsMu 内调 restSeedPositions 死锁）
	var needsReseed bool
	w.positionsMu.Lock()
	for _, patch := range msg.Partial {
		if w.applyPatchLocked(ctx, patch) {
			needsReseed = true
		}
	}
	w.positionsMu.Unlock()
	if needsReseed {
		jwt, err := w.auth.JWT(ctx)
		if err != nil {
			log.Printf("[cmc-ws] patch reseed JWT: %v", err)
		} else {
			w.restSeedPositions(ctx, jwt)
		}
	}
	for _, patch := range msg.Partial {
		if strings.Contains(patch.P, "/orders") {
			w.kickHistoryDebounced()
			break
		}
	}
	w.extractPriceEvent(ctx)
	w.scheduleFlush()
	w.recomputeAndFanout(ctx)
}

// applyPatchLocked D-225 v2.1：三深度路径分发（调用方持 positionsMu）。
// /positions/i（position 字段 merge）/ /positions/i/orders/j（order 字段 merge）/ /（根总额→账户侧缓存）。
// t!="m"、索引越界、未知路径 → log warn+计数器；返回 true 表示需 REST 重播种（调用方在解锁后执行）。
func (w *WsClient) applyPatchLocked(ctx context.Context, patch wsPatch) bool {
	parts := strings.Split(strings.TrimPrefix(patch.P, "/"), "/")
	// 根路径：/ → 根总额（更新账户侧缓存，当前 lastSummary 不含根字段，仅 log）
	if len(parts) == 1 && parts[0] == "" {
		if patch.T != "m" {
			w.patchWarnCount.Add(1)
			log.Printf("[cmc-ws] patch warn: path=/ t=%s (count=%d) → REST reseed", patch.T, w.patchWarnCount.Load())
			return true
		}
		return false // 根总额字段当前不落库，静默接受
	}
	if len(parts) < 2 || parts[0] != "positions" {
		w.patchWarnCount.Add(1)
		log.Printf("[cmc-ws] patch warn: path=%s t=%s (count=%d) → REST reseed", patch.P, patch.T, w.patchWarnCount.Load())
		return true
	}
	idx, err := strconv.Atoi(parts[1])
	if err != nil || idx < 0 || idx >= len(w.positions) {
		w.patchWarnCount.Add(1)
		log.Printf("[cmc-ws] patch warn: path=%s t=%s (count=%d) → REST reseed", patch.P, patch.T, w.patchWarnCount.Load())
		return true
	}
	pos := &w.positions[idx]
	// /positions/i/orders/j → order 字段 merge
	if len(parts) >= 4 && parts[2] == "orders" {
		orderIdx, oerr := strconv.Atoi(parts[3])
		if oerr != nil || orderIdx < 0 || orderIdx >= len(pos.Orders) {
			w.patchWarnCount.Add(1)
			log.Printf("[cmc-ws] patch warn: path=%s t=%s (count=%d) → REST reseed", patch.P, patch.T, w.patchWarnCount.Load())
			return true
		}
		if patch.T != "m" {
			w.patchWarnCount.Add(1)
			log.Printf("[cmc-ws] patch warn: path=%s t=%s (count=%d) → REST reseed", patch.P, patch.T, w.patchWarnCount.Load())
			return true
		}
		var d map[string]json.RawMessage
		if err := json.Unmarshal(patch.D, &d); err != nil {
			return false
		}
		mergeOrderFields(&pos.Orders[orderIdx], d)
		return false
	}
	// /positions/i → position 字段 merge
	if patch.T != "m" {
		w.patchWarnCount.Add(1)
		log.Printf("[cmc-ws] patch warn: path=%s t=%s (count=%d) → REST reseed", patch.P, patch.T, w.patchWarnCount.Load())
		return true
	}
	var d map[string]json.RawMessage
	if err := json.Unmarshal(patch.D, &d); err != nil {
		return false
	}
	mergePositionFields(pos, d)
	return false
}
