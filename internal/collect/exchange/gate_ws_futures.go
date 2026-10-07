// gate_ws_futures.go —— Gate futures 公共连接帧域（D-570 包C：自 gate_ws.go 拆出保
// check-lines <300）。tickers（mark 近似报价+funding 帧职责，D-509）/book_ticker（真实盘口 S8）
// /contract_info（规格失效推送 S6④）/adl_warning（ADL 预警 S7）四频道同连接分派。
package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"arb/internal/bus"
	"arb/internal/fact"
)

// handleFuturesDispatch D-570 包C：futures 连接多频道分派（channel 字段路由；
// 未知频道静默——公共帧无审计表，WarnUnconsumed 是私有域机制）。
func (g *GateWebSocket) handleFuturesDispatch(ctx context.Context, data []byte, publish func(bus.Quote)) {
	var env struct {
		Channel string `json:"channel"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return
	}
	switch env.Channel {
	case "futures.tickers":
		g.handleFutures(ctx, data, publish)
	case "futures.book_ticker":
		g.handleBookTicker(data, publish)
	case "futures.contract_info":
		g.onContractInfoFrame(data)
	case "futures.adl_warning":
		g.onADLWarning(data)
	}
}

// handleBookTicker D-570 包C S8：futures.book_ticker 真形帧 {t,u,s,b,B,a,A}——
// s=contract、b/a=最优买/卖价串（空串=空边 skip 整帧，快照 :1945 钉死）；mark 近似保留为断流兜底
// （两源同键 fresher wins，fresher 由 QuoteBus Time 决定）。
func (g *GateWebSocket) handleBookTicker(data []byte, publish func(bus.Quote)) {
	var msg struct {
		Event  string                     `json:"event"`
		Result map[string]json.RawMessage `json:"result"`
	}
	// result 走 map 精确键查表——struct tag "b" 会被同帧 "B"（量）大小写不敏感
	// 匹配覆写（D-569 "e"/"E" 碰撞同病灶，实弹帧 b/B/a/A 四键并存必踩）。
	if err := json.Unmarshal(data, &msg); err != nil || msg.Event != "update" {
		return
	}
	var s, bp, ap string
	_ = json.Unmarshal(msg.Result["s"], &s)
	_ = json.Unmarshal(msg.Result["b"], &bp)
	_ = json.Unmarshal(msg.Result["a"], &ap)
	if s == "" || bp == "" || ap == "" {
		return // 空边帧 skip（不崩不发布——半盘口非报价）
	}
	bid, berr := f64z(bp)
	ask, aerr := f64z(ap)
	if berr != nil || aerr != nil || bid <= 0 || ask <= 0 {
		return
	}
	ts := time.Now()
	var tms int64
	if json.Unmarshal(msg.Result["t"], &tms) == nil && tms > 0 {
		ts = time.UnixMilli(tms)
	}
	publish(bus.Quote{
		Symbol:      canonicalFromGate(s),
		Venue:       VenueGate,
		VenueType:   "perp",
		VenueSymbol: s,
		Bid:         bid,
		Ask:         ask,
		Time:        ts,
	})
}

// onContractInfoFrame D-570 包C S6④：futures.contract_info 帧（event=all 首帧与
// update 同路）——按 result.contract/name 逐合约调失效回调。result 单对象/数组双容。
func (g *GateWebSocket) onContractInfoFrame(data []byte) {
	if g.onContractInfo == nil {
		return
	}
	var msg struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(data, &msg); err != nil || len(msg.Result) == 0 {
		return
	}
	var items []struct {
		Contract string `json:"contract"`
		Name     string `json:"name"` // 宽容：实弹字段名 contract/name 双容（首帧 WARN 核对域外，失效漏=静默）
	}
	if err := json.Unmarshal(msg.Result, &items); err != nil {
		var one struct {
			Contract string `json:"contract"`
			Name     string `json:"name"`
		}
		if err2 := json.Unmarshal(msg.Result, &one); err2 != nil {
			return
		}
		items = append(items, struct {
			Contract string `json:"contract"`
			Name     string `json:"name"`
		}{Contract: one.Contract, Name: one.Name})
	}
	for _, it := range items {
		c := it.Contract
		if c == "" {
			c = it.Name
		}
		if c != "" {
			g.onContractInfo(c)
		}
	}
}

// onADLWarning D-570 包C S7 / D-101：futures.adl_warning 帧（合约级市场广播）持仓
// 关联过滤——state=queued/deleveraged（事实态）且命中本 venue 活跃仓 OPEN 腿才
// critical；state=warning 属预告非事实、非持仓合约的广播一律降 info 记档（告警
// 面板可查）；state=normal 恢复帧不告（原样）。payload=result 原文截 500 防巨帧。
// 探针未接线/帧缺合约=关联不明 fail-safe 视同命中（宁多告不漏告——禁把真风险
// 静默降档；此时 warning 仍 info：预告非事实与关联无关）。
func (g *GateWebSocket) onADLWarning(data []byte) {
	var msg struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(data, &msg); err != nil || len(msg.Result) == 0 {
		return
	}
	var fr struct {
		Contract string `json:"contract"`
		State    string `json:"state"`
	}
	if err := json.Unmarshal(msg.Result, &fr); err != nil || fr.State == "" || fr.State == "normal" {
		return
	}
	if g.cfg.AlertFn == nil {
		return
	}
	associated := true
	if g.cfg.ADLPositionHit != nil && fr.Contract != "" {
		associated = g.cfg.ADLPositionHit(fr.Contract)
	}
	level := "info"
	if associated && (fr.State == "queued" || fr.State == "deleveraged") {
		level = "critical"
	}
	payload := string(msg.Result)
	if len(payload) > 500 {
		payload = payload[:500]
	}
	rel := "否"
	if associated {
		rel = "是"
	}
	g.cfg.AlertFn(level, fmt.Sprintf("gate ADL 风险提示（state=%s·持仓关联=%s）\n原始明细：%s", fr.State, rel, payload))
}

// gateFuturesUpdate futures.tickers 推送：result 为数组，无 bid/ask，用 mark_price 近似。
// D-509 S3 实测字段（fx-ws 公共帧）：funding_rate/funding_next_apply(unix s)/
// funding_interval(s) 原生在帧——funding 三件套自帧内取（rate+next_apply），interval
// fact 归日轮 gate_contract_meta（S5 起唯一职责=元数据；帧内 funding_interval 仍用于年化折算）。
type gateFuturesUpdate struct {
	Event  string `json:"event"`
	Result []struct {
		Contract         string  `json:"contract"`
		MarkPrice        string  `json:"mark_price"`
		T                int64   `json:"t"` // unix ms
		FundingRate      string  `json:"funding_rate"`
		FundingNextApply float64 `json:"funding_next_apply"` // unix s
		FundingInterval  float64 `json:"funding_interval"`   // s
	} `json:"result"`
}

// handleFutures 解析 futures.tickers 更新并发布报价。
func (g *GateWebSocket) handleFutures(ctx context.Context, data []byte, publish func(bus.Quote)) {
	var msg gateFuturesUpdate
	if err := json.Unmarshal(data, &msg); err != nil || msg.Event != "update" {
		return
	}
	for _, item := range msg.Result {
		if item.Contract == "" {
			continue
		}
		var mark float64
		fmt.Sscanf(item.MarkPrice, "%f", &mark)
		if mark == 0 {
			continue
		}
		ts := time.Now()
		if item.T > 0 {
			ts = time.UnixMilli(item.T)
		}
		publish(bus.Quote{
			Symbol:      canonicalFromGate(item.Contract),
			Venue:       VenueGate,
			VenueType:   "perp",
			VenueSymbol: item.Contract,
			Bid:         mark,
			Ask:         mark,
			Time:        ts,
		})
		// D-509 S3：帧内原生 funding 字段→fact（缺字段=缺省不造，venue 禁借用律）。
		if g.emitFacts != nil && item.FundingRate != "" {
			var rate float64
			if _, err := fmt.Sscanf(item.FundingRate, "%f", &rate); err != nil {
				log.Printf("[gate-ws] funding rate %q dirty: %v", item.FundingRate, err)
				continue
			}
			sym := canonicalFromGate(item.Contract)                      // BTCUSDT 形（与 REST 键 sym+USDT 同形）
			periods := fundingPeriodsPerDay(item.FundingInterval * 1000) // 帧内原生 interval
			fs := []fact.Fact{
				{Kind: fact.KindFunding, Venue: VenueGate, Symbol: sym,
					Value: annualizeFunding(rate, periods), Unit: fact.UnitPctAnnualized,
					Ts: ts, Src: "gate_ws_futures"},
			}
			if item.FundingNextApply > 0 {
				fs = append(fs, fact.Fact{Kind: fact.KindFundingNextSettle, Venue: VenueGate,
					Symbol: sym, Value: item.FundingNextApply, Unit: fact.UnitUnixSec, Ts: ts, Src: "gate_ws_futures"})
			}
			if err := g.emitFacts(ctx, fs); err != nil {
				log.Printf("[gate-ws] funding emit %s: %v", item.Contract, err)
			}
		}
	}
}
