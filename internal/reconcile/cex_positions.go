// cex_positions.go — CEX perp 持仓查询器（D-121）。
// 实现 VenueQuerier 接口，查 OKX/Gate/Binance perp 全部持仓 → []VenueOrder。
// 注册进 BrokerManager → BrokerPositionMonitor 轮询 → enrichPositions 匹配 → 前端显示浮盈/funding。
//
// CEX perp 持仓是聚合仓位（多单聚合），无单一 ticket → Ticket=Symbol（venue 原生符号），
// buildPositionIDIndex 扩展按 venueKey+symbol 索引匹配 positionId。
package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"arb/internal/exec"
	"arb/internal/httpx"
)

// CEXPositionQuerier 查询 CEX perp 持仓（实现 VenueQuerier）。
// 字段同 CEXAccountQuerier（复用签名逻辑），但查持仓而非余额。
type CEXPositionQuerier struct {
	venue      string // okx / gate / binance
	venueType  string // perp（spot 无持仓查询）
	apiKey     string
	apiSecret  string
	passphrase string // OKX 必填
	baseURL    string
	simulated  bool
	client     *http.Client
}

// NewCEXPositionQuerier 创建 CEX perp 持仓查询器。
func NewCEXPositionQuerier(venue, venueType, apiKey, apiSecret, passphrase, baseURL string, simulated bool) *CEXPositionQuerier {
	return &CEXPositionQuerier{
		venue:      venue,
		venueType:  venueType,
		apiKey:     apiKey,
		apiSecret:  apiSecret,
		passphrase: passphrase,
		baseURL:    baseURL,
		simulated:  simulated,
		client:     httpx.Client(), // CA 信任 + X-Proxy-Token（Binance demo-fapi 自签证书，D-055 同款）
	}
}

func (q *CEXPositionQuerier) VenueKey() string { return q.venue + ":" + q.venueType }

// QueryOpenedOrders 查询全部 perp 持仓 → []VenueOrder。
// spot venue 返回空（无持仓概念）；perp 按 venue 分派。
func (q *CEXPositionQuerier) QueryOpenedOrders(ctx context.Context) ([]VenueOrder, error) {
	if q.venueType != "perp" {
		return nil, nil // spot 无持仓查询
	}
	switch q.venue {
	case "okx":
		return q.okxPositions(ctx)
	case "gate":
		return q.gatePositions(ctx)
	case "binance":
		return q.binancePositions(ctx)
	default:
		return nil, fmt.Errorf("cex positions: unsupported venue %q", q.venue)
	}
}

// === OKX ===
// GET /api/v5/account/positions?instType=SWAP — 全部 SWAP 持仓。
// long_short_mode：long/short 分列；pos=合约数（张）；upl=浮盈（USDT）；fee=累计手续费。
// D-146 S5 微件③ 返修：保证金优先 imr（初始保证金）→margin 回落；mmr→MaintMargin。
// D-146 微件③ 追加：sim 环境 imr 实证为恒定假值（$1 名义也占 ~$333），demo 置 0（宁缺毋假）。
func (q *CEXPositionQuerier) okxPositions(ctx context.Context) ([]VenueOrder, error) {
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
	if q.simulated {
		headers.Set("x-simulated-trading", "1")
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
	var out []VenueOrder
	for _, p := range resp.Data {
		pos, _ := strconv.ParseFloat(p.Pos, 64)
		if pos == 0 {
			continue
		}
		avgPx, _ := strconv.ParseFloat(p.AvgPx, 64)
		upl, _ := strconv.ParseFloat(p.Upl, 64)
		fee, _ := strconv.ParseFloat(p.Fee, 64)
		side := "BUY"
		if p.PosSide == "short" {
			side = "SELL"
		}
		ctimeMs, _ := strconv.ParseInt(p.CTime, 10, 64)
		openedAt := time.UnixMilli(ctimeMs)
		// D-146 S5 微件③ 返修：优先 imr（初始保证金），margin 回落
		initMargin, _ := strconv.ParseFloat(p.Imr, 64)
		if initMargin == 0 {
			initMargin, _ = strconv.ParseFloat(p.Margin, 64) // 回落：imr 缺失时用 margin（当前占用）
		}
		// D-146 微件③ 追加：sim 环境 imr 实证为恒定假值（$1 名义也占 ~$333，2026-08-29 业主实证
		// 「okx 保证金都这么高」）——demo 不展示（宁缺毋假），真金（simulated=false）自然恢复真值。
		if q.simulated {
			initMargin = 0
		}
		maintMargin, _ := strconv.ParseFloat(p.Mmr, 64)
		out = append(out, VenueOrder{
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
// GET /futures/usdt/positions — 全部 USDT 本位合约持仓（数组）。
// size > 0 多头 / < 0 空头；entry_price=开仓价；unrealized_pnl=浮盈。
func (q *CEXPositionQuerier) gatePositions(ctx context.Context) ([]VenueOrder, error) {
	const endpoint = "/futures/usdt/positions"
	urlPath := "/api/v4" + endpoint
	ts := time.Now().Unix()
	sign := exec.GateSign(q.apiSecret, "GET", urlPath, "", "", ts)
	headers := http.Header{
		"KEY":       {q.apiKey},
		"Timestamp": {strconv.FormatInt(ts, 10)},
		"SIGN":      {sign},
	}
	body, err := cexGet(ctx, q.client, q.baseURL+endpoint, headers)
	if err != nil {
		return nil, fmt.Errorf("gate positions: %w", err)
	}
	// Gate 返回数组（ListPositions），非单对象
	var positions []struct {
		Contract      string `json:"contract"`
		Size          int64  `json:"size"` // >0 多 / <0 空
		EntryPrice    string `json:"entry_price"`
		UnrealizedPnl string `json:"unrealized_pnl"`
		Funding       string `json:"funding"` // 累计 funding
	}
	if err := json.Unmarshal(body, &positions); err != nil {
		return nil, fmt.Errorf("gate positions json: %w", err)
	}
	var out []VenueOrder
	// D-121：Gate 持仓 size=合约数，需乘 quanto_multiplier 转基础币（与 OKX/Binance Volume 单位对齐）。
	quantoCache := make(map[string]float64)
	for _, p := range positions {
		if p.Size == 0 {
			continue
		}
		entryPx, _ := strconv.ParseFloat(p.EntryPrice, 64)
		upl, _ := strconv.ParseFloat(p.UnrealizedPnl, 64)
		funding, _ := strconv.ParseFloat(p.Funding, 64)
		side, posSide := "BUY", "long" // D-195-C2：Gate size>0=long/BUY, <0=short/SELL
		vol := float64(p.Size)
		if p.Size < 0 {
			side, posSide, vol = "SELL", "short", -vol
		}
		// 查合约规格转基础币（缓存避免重复请求）
		quanto, ok := quantoCache[p.Contract]
		if !ok {
			quanto = gateQuantoMultiplier(ctx, q.client, q.baseURL, q.apiKey, q.apiSecret, p.Contract)
			quantoCache[p.Contract] = quanto
		}
		vol = vol * quanto // 合约数 × quanto = 基础币数量
		out = append(out, VenueOrder{
			Ticket:    p.Contract, // CEX 聚合仓位 → 用 symbol
			Symbol:    p.Contract,
			Side:      side,
			Volume:    vol,
			OpenPrice: entryPx,
			Venue:     q.venue,
			VenueKey:  q.VenueKey(),
			Profit:    upl,
			Swap:      funding, // Gate funding ≈ swap
			PosSide:   posSide, // D-195-C2：从 size 正负推导
		})
	}
	return out, nil
}

// === Binance ===
// GET /fapi/v2/positionRisk — 全部 USDT-M 合约持仓（含 0 仓位）。
// positionAmt > 0 多头 / < 0 空头（base currency）；unRealizedProfit=浮盈。
//
// D-146 S5 微件③ 返修：positionRisk 不返回 initialMargin/maintMargin（账户级 /fapi/v2/account 字段，
// 字段想当然错配→保证金恒 0）。per-position 保证金：逐仓→isolatedWallet；全仓→notional/leverage。
// MaintMargin=0（positionRisk 无此字段，走 D-124 账户分摊，与 MT 一致）。
func (q *CEXPositionQuerier) binancePositions(ctx context.Context) ([]VenueOrder, error) {
	params := "timestamp=" + strconv.FormatInt(time.Now().UnixMilli(), 10) + "&recvWindow=5000"
	sig := binanceSign(q.apiSecret, params)
	url := q.baseURL + "/v2/positionRisk?" + params + "&signature=" + sig
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-MBX-APIKEY", q.apiKey)
	resp, err := q.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("binance positions: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("binance positions: http %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var positions []struct {
		Symbol           string `json:"symbol"`
		PositionAmt      string `json:"positionAmt"`
		EntryPrice       string `json:"entryPrice"`
		UnRealizedProfit string `json:"unRealizedProfit"`
		PositionSide     string `json:"positionSide"`   // BOTH / LONG / SHORT (hedge mode)
		MarginType       string `json:"marginType"`     // isolated / cross
		IsolatedWallet   string `json:"isolatedWallet"` // 逐仓钱包余额（=占用保证金，isolated 时权威）
		Leverage         string `json:"leverage"`       // 杠杆倍数
		Notional         string `json:"notional"`       // 名义价值（USDT，全仓保证金=notional/leverage）
	}
	if err := json.Unmarshal(data, &positions); err != nil {
		return nil, fmt.Errorf("binance positions json: %w", err)
	}
	var out []VenueOrder
	for _, p := range positions {
		amt, _ := strconv.ParseFloat(p.PositionAmt, 64)
		if amt == 0 {
			continue
		}
		entryPx, _ := strconv.ParseFloat(p.EntryPrice, 64)
		upl, _ := strconv.ParseFloat(p.UnRealizedProfit, 64)
		side, posSide := "BUY", "long" // D-195-C2：Binance amt>0=long/BUY, <0=short/SELL
		vol := amt
		if amt < 0 {
			side, posSide, vol = "SELL", "short", -vol
		}
		// D-146 S5 微件③ 返修：逐仓→isolatedWallet；全仓→notional/leverage
		var initMargin float64
		if p.MarginType == "isolated" {
			initMargin, _ = strconv.ParseFloat(p.IsolatedWallet, 64)
		} else {
			notional, _ := strconv.ParseFloat(p.Notional, 64)
			if lev, _ := strconv.ParseFloat(p.Leverage, 64); lev > 0 {
				initMargin = notional / lev
			}
		}
		out = append(out, VenueOrder{
			Ticket: p.Symbol, Symbol: p.Symbol, Side: side, Volume: vol,
			OpenPrice: entryPx, Venue: q.venue, VenueKey: q.VenueKey(), Profit: upl,
			InitialMargin: initMargin, // MaintMargin=0：positionRisk 无此字段，走 D-124 账户分摊
			PosSide:       posSide,    // D-195-C2：从 amt 正负推导
		})
	}
	return out, nil
}
