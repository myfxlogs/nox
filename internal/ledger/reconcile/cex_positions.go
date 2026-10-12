// cex_positions.go — CEX perp 持仓查询器（D-121）：实现 venue.Querier 查 OKX/Gate/Binance
// perp 全部持仓 → []venue.Order → BrokerPositionMonitor 轮询 → enrichPositions → 前端显示。
// CEX perp 持仓是聚合仓位（无单一 ticket）→ Ticket=Symbol；positionId 索引按 venueKey+symbol 匹配。
package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"nox/internal/data/venue"
	"nox/internal/platform/signutil"
)

// === OKX ===
// GET /api/v5/account/positions?instType=SWAP — 全部 SWAP 持仓。long_short_mode：long/short 分列；
// pos=合约数（张）；upl=浮盈；fee=累计手续费。D-146 S5 微件③：保证金优先 imr→margin 回落、mmr→MaintMargin；
// sim 环境 imr 实证为恒定假值（$1 名义也占 ~$333），demo 置 0（宁缺毋假）。
func (q *CEXPositionQuerier) okxPositions(ctx context.Context) ([]venue.Order, error) {
	const path = "/api/v5/account/positions?instType=SWAP"
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	prehash := ts + "GET" + path
	sign := okxSign(q.apiSecret, prehash)
	headers := http.Header{
		"OK-ACCESS-KEY":        {q.apiKey},
		"OK-ACCESS-SIGN":       {sign},
		"OK-ACCESS-TIMESTAMP":  {ts},
		"OK-ACCESS-PASSPHRASE": {q.passphrase},
		"Content-Type":         {"application/json"},
	}
	body, err := cexGet(ctx, q.client, q.baseURL+path, headers)
	if err != nil {
		return nil, fmt.Errorf("okx positions: %w", err)
	}
	var resp struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			InstID  string `json:"instId"`
			PosSide string `json:"posSide"` // long / short / net
			Pos     string `json:"pos"`     // 合约数（张）
			AvgPx   string `json:"avgPx"`
			Upl     string `json:"upl"`    // 浮盈（USDT）
			Fee     string `json:"fee"`    // 累计手续费（负数）
			CTime   string `json:"cTime"`  // 开仓时间 ms
			Margin  string `json:"margin"` // 占用保证金（USDT，当前值）
			Imr     string `json:"imr"`    // D-146 S5 微件③ 返修：初始保证金（开仓快照，优先）
			Mmr     string `json:"mmr"`    // D-146 S5 微件③ 返修：维持保证金
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("okx positions json: %w", err)
	}
	if resp.Code != "0" {
		return nil, fmt.Errorf("okx positions: code=%s msg=%s", resp.Code, resp.Msg)
	}
	var out []venue.Order
	for _, p := range resp.Data {
		pos, perr := f64z(p.Pos)
		if perr != nil {
			return nil, fmt.Errorf("parse %s=%q: %w", "p.Pos", p.Pos, perr)
		}
		if pos == 0 {
			continue
		}
		avgPx, perr := f64z(p.AvgPx)
		if perr != nil {
			return nil, fmt.Errorf("parse %s=%q: %w", "p.AvgPx", p.AvgPx, perr)
		}
		upl, perr := f64z(p.Upl)
		if perr != nil {
			return nil, fmt.Errorf("parse %s=%q: %w", "p.Upl", p.Upl, perr)
		}
		fee, perr := f64z(p.Fee)
		if perr != nil {
			return nil, fmt.Errorf("parse %s=%q: %w", "p.Fee", p.Fee, perr)
		}
		side := "BUY"
		if p.PosSide == "short" {
			side = "SELL"
		}
		ctimeMs, _ := strconv.ParseInt(p.CTime, 10, 64)
		openedAt := time.UnixMilli(ctimeMs)
		initMargin, perr := f64z(p.Imr)
		if perr != nil {
			return nil, fmt.Errorf("parse %s=%q: %w", "p.Imr", p.Imr, perr)
		} // D-146 S5：imr（初始保证金）优先，margin 回落
		if initMargin == 0 {
			if pv, perr := f64z(p.Margin); perr != nil {
				return nil, fmt.Errorf("parse %s=%q: %w", "p.Margin", p.Margin, perr)
			} else {
				initMargin = pv
			}
		}
		// sim 环境 imr 实证为恒定假值（$1 名义也占 ~$333）——demo 置 0 宁缺毋假，真金自然恢复。
		if q.simulated {
			initMargin = 0
		}
		maintMargin, perr := f64z(p.Mmr)
		if perr != nil {
			return nil, fmt.Errorf("parse %s=%q: %w", "p.Mmr", p.Mmr, perr)
		}
		out = append(out, venue.Order{
			Ticket:        p.InstID, // CEX 聚合仓位无单一 ticket → 用 symbol
			Symbol:        p.InstID,
			Side:          side,
			Volume:        pos,
			OpenPrice:     avgPx,
			Venue:         q.venue,
			VenueKey:      q.VenueKey(),
			Profit:        upl,
			Commission:    fee, // OKX fee 是累计手续费（负数=成本）
			OpenedAt:      openedAt,
			InitialMargin: initMargin,  // D-146 S5 微件③ 返修：imr 优先，margin 回落
			MaintMargin:   maintMargin, // D-146 S5 微件③ 返修：mmr
			PosSide:       p.PosSide,   // D-195-C2：OKX long_short_mode position_side
		})
	}
	return out, nil
}

// === Gate ===
// GET /futures/usdt/positions — 全部 USDT 本位合约持仓（数组）。size>0 多/<0 空。
// 字段名实证修正：unrealised_pnl=浮盈（英式拼写）、pnl_fund=仓累计资金费、
// initial_margin 优先 margin 回落（cross 模式 margin 恒 0，镜像 okx imr→margin）、maintenance_margin→MaintMargin。
func (q *CEXPositionQuerier) gatePositions(ctx context.Context) ([]venue.Order, error) {
	const endpoint = "/futures/usdt/positions"
	urlPath := "/api/v4" + endpoint
	ts := time.Now().Unix()
	sign := signutil.GateSign(q.apiSecret, "GET", urlPath, "", "", ts)
	headers := http.Header{
		"KEY":       {q.apiKey},
		"Timestamp": {strconv.FormatInt(ts, 10)},
		"SIGN":      {sign},
	}
	body, err := cexGet(ctx, q.client, q.baseURL+endpoint, headers)
	if err != nil {
		return nil, fmt.Errorf("gate positions: %w", err)
	}
	var positions []struct { // Gate 返回数组（ListPositions），非单对象
		Contract      string `json:"contract"`
		Size          int64  `json:"size"` // >0 多 / <0 空
		EntryPrice    string `json:"entry_price"`
		UnrealisedPnl string `json:"unrealised_pnl"`     // D-311 R8①：英式拼写实测字段名
		PnlFund       string `json:"pnl_fund"`           // D-311 R8②：仓累计资金费
		InitialMargin string `json:"initial_margin"`     // D-311 R8③：初始保证金（优先）
		Margin        string `json:"margin"`             // D-311 R8③：回落字段（cross 模式恒 0）
		MaintMargin   string `json:"maintenance_margin"` // D-311 R8④：维持保证金
	}
	if err := json.Unmarshal(body, &positions); err != nil {
		return nil, fmt.Errorf("gate positions json: %w", err)
	}
	var out []venue.Order
	// Gate 持仓 size=合约数，需乘 quanto_multiplier 转基础币（与 OKX/Binance Volume 单位对齐）。
	quantoCache := make(map[string]float64)
	for _, p := range positions {
		if p.Size == 0 {
			continue
		}
		entryPx, perr := f64z(p.EntryPrice)
		if perr != nil {
			return nil, fmt.Errorf("parse %s=%q: %w", "p.EntryPrice", p.EntryPrice, perr)
		}
		upl, perr := f64z(p.UnrealisedPnl)
		if perr != nil {
			return nil, fmt.Errorf("parse %s=%q: %w", "p.UnrealisedPnl", p.UnrealisedPnl, perr)
		}
		funding, perr := f64z(p.PnlFund)
		if perr != nil {
			return nil, fmt.Errorf("parse %s=%q: %w", "p.PnlFund", p.PnlFund, perr)
		}
		side, posSide := "BUY", "long" // D-195-C2：Gate size>0=long/BUY, <0=short/SELL
		vol := float64(p.Size)
		if p.Size < 0 {
			side, posSide, vol = "SELL", "short", -vol
		}
		quanto, ok := quantoCache[p.Contract] // 查合约规格转基础币（缓存避免重复请求）
		if !ok {
			quanto = gateQuantoMultiplier(ctx, q.client, q.baseURL, q.apiKey, q.apiSecret, p.Contract)
			quantoCache[p.Contract] = quanto
		}
		vol = vol * quanto // 合约数 × quanto = 基础币数量
		initMargin, perr := f64z(p.InitialMargin)
		if perr != nil {
			return nil, fmt.Errorf("parse %s=%q: %w", "p.InitialMargin", p.InitialMargin, perr)
		} // R8③：initial_margin 优先，margin 回落
		if initMargin == 0 {
			if pv, perr := f64z(p.Margin); perr != nil {
				return nil, fmt.Errorf("parse %s=%q: %w", "p.Margin", p.Margin, perr)
			} else {
				initMargin = pv
			}
		}
		maintMargin, perr := f64z(p.MaintMargin)
		if perr != nil {
			return nil, fmt.Errorf("parse %s=%q: %w", "p.MaintMargin", p.MaintMargin, perr)
		}
		out = append(out, venue.Order{
			Ticket:        p.Contract, // CEX 聚合仓位 → 用 symbol
			Symbol:        p.Contract,
			Side:          side,
			Volume:        vol,
			OpenPrice:     entryPx,
			Venue:         q.venue,
			VenueKey:      q.VenueKey(),
			Profit:        upl,
			Swap:          funding, // D-311 R8②：pnl_fund 仓累计资金费，供 swapAccrued/达成分子
			PosSide:       posSide, // D-195-C2：从 size 正负推导
			InitialMargin: initMargin,
			MaintMargin:   maintMargin, // D-311 R8④
		})
	}
	return out, nil
}
