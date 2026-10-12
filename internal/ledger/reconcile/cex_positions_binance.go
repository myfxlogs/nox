// cex_positions_binance.go — Binance perp 持仓查询（D-121；cex_positions.go 拆出，D-508 S2b 行数归位）。
package reconcile

import (
	"arb/internal/data/venue"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// === Binance ===
// GET /fapi/v2/positionRisk — 全部 USDT-M 合约持仓（含 0 仓位）。positionAmt>0 多/<0 空；unRealizedProfit=浮盈。
// positionRisk 无 initialMargin/maintMargin（账户级字段）——逐仓→isolatedWallet、
// 全仓→|notional|/leverage（D-343：空头 notional 负值取绝对值）；MaintMargin=0 走 D-124 账户分摊。
func (q *CEXPositionQuerier) binancePositions(ctx context.Context) ([]venue.Order, error) {
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
		Notional         string `json:"notional"`       // 名义价值（USDT，空头为负；全仓保证金=|notional|/leverage）
	}
	if err := json.Unmarshal(data, &positions); err != nil {
		return nil, fmt.Errorf("binance positions json: %w", err)
	}
	var out []venue.Order
	for _, p := range positions {
		amt, perr := f64z(p.PositionAmt)
		if perr != nil {
			return nil, fmt.Errorf("parse %s=%q: %w", "p.PositionAmt", p.PositionAmt, perr)
		}
		if amt == 0 {
			continue
		}
		entryPx, perr := f64z(p.EntryPrice)
		if perr != nil {
			return nil, fmt.Errorf("parse %s=%q: %w", "p.EntryPrice", p.EntryPrice, perr)
		}
		upl, perr := f64z(p.UnRealizedProfit)
		if perr != nil {
			return nil, fmt.Errorf("parse %s=%q: %w", "p.UnRealizedProfit", p.UnRealizedProfit, perr)
		}
		side, posSide := "BUY", "long" // D-195-C2：Binance amt>0=long/BUY, <0=short/SELL
		vol := amt
		if amt < 0 {
			side, posSide, vol = "SELL", "short", -vol
		}
		// 微件③ 返修：逐仓→isolatedWallet；全仓→notional/leverage
		var initMargin float64
		if p.MarginType == "isolated" {
			if pv, perr := f64z(p.IsolatedWallet); perr != nil {
				return nil, fmt.Errorf("parse %s=%q: %w", "p.IsolatedWallet", p.IsolatedWallet, perr)
			} else {
				initMargin = pv
			}
		} else {
			notional, perr := f64z(p.Notional)
			if perr != nil {
				return nil, fmt.Errorf("parse %s=%q: %w", "p.Notional", p.Notional, perr)
			}
			lev, perr := f64z(p.Leverage)
			if perr != nil {
				return nil, fmt.Errorf("parse %s=%q: %w", "p.Leverage", p.Leverage, perr)
			}
			if lev > 0 {
				initMargin = math.Abs(notional) / lev // D-343：空头腿 notional 负值→Abs（符号坑，负 margin 被下游 >0 守卫拒）
			}
		}
		out = append(out, venue.Order{
			Ticket: p.Symbol, Symbol: p.Symbol, Side: side, Volume: vol,
			OpenPrice: entryPx, Venue: q.venue, VenueKey: q.VenueKey(), Profit: upl,
			InitialMargin: initMargin, // MaintMargin=0：positionRisk 无此字段，走 D-124 账户分摊
			PosSide:       posSide,    // D-195-C2：从 amt 正负推导
		})
	}
	q.fillBinanceOpenedAt(ctx, out) // D-037：positionRisk 无开仓时间→userTrades 推导
	return out, nil
}

// === D-037：binance openedAt 推导 ===
// positionRisk 无开仓时间（OpenedAt 零值 → income 窗无界 → swap/净估缺息）。reconcile 时
// 经 GET /fapi/v1/userTrades 推导「最近一次 positionAmt 由 0 变非 0 的成交时刻」。
// 每 symbol 一次 + 持仓集变化失效（querier 级缓存）：在仓期间 openedAt 不变 → 一次拉取；
// 平仓离集/换向 → 逐出，重开重新推导。ts 零值=不可得负缓存（1000 笔窗内未见 0 过渡），
// venue 查询失败不缓存→下轮重试。禁每帧：缓存命中零 HTTP。

// openedAtEntry 开仓时刻缓存项；ts 零值=推导不可得（负缓存防每轮重打 venue）。
type openedAtEntry struct {
	ts time.Time
}

// fillBinanceOpenedAt 持仓集签名校验缓存 → miss 逐个 userTrades 推导 → 回填 orders.OpenedAt。
// 缓存键=symbol|side（对冲模式同 symbol LONG+SHORT 双仓各有开仓点，不共享条目）。
func (q *CEXPositionQuerier) fillBinanceOpenedAt(ctx context.Context, orders []venue.Order) {
	live := map[string]venue.Order{}
	for _, o := range orders {
		live[o.Symbol+"|"+o.Side] = o
	}
	q.openedAtMu.Lock()
	if q.openedAtCache == nil {
		q.openedAtCache = map[string]openedAtEntry{}
	}
	for k := range q.openedAtCache {
		if _, ok := live[k]; !ok {
			delete(q.openedAtCache, k) // 离集/换向 → 失效（重开必重新推导）
		}
	}
	if len(orders) == 0 {
		q.openedAtMu.Unlock()
		return
	}
	var miss []string
	for k := range live {
		if _, ok := q.openedAtCache[k]; !ok {
			miss = append(miss, k)
		}
	}
	q.openedAtMu.Unlock()
	for _, k := range miss {
		o := live[k]
		ts, err := q.binanceOpenedAt(ctx, o.Symbol, o.Side)
		if err != nil {
			log.Printf("[reconcile] binance openedAt %s %s: %v", o.Symbol, o.Side, err)
			continue // 查询失败不缓存→下轮重试；OpenedAt 缺席=诚实空态
		}
		q.openedAtMu.Lock()
		q.openedAtCache[k] = openedAtEntry{ts: ts}
		q.openedAtMu.Unlock()
	}
	q.openedAtMu.Lock()
	for i := range orders {
		orders[i].OpenedAt = q.openedAtCache[orders[i].Symbol+"|"+orders[i].Side].ts
	}
	q.openedAtMu.Unlock()
}

// binanceOpenedAt userTrades 推导开仓时刻：按 positionSide 桶累 signed qty，
// 记「最近一次 |amt| 0→非0 的成交时刻」=当前持仓开仓点（平后再开取后者）。
// 单向模式全桶 BOTH（BUY+/SELL−）；对冲模式 SHORT 桶 SELL+/BUY−。
// limit=1000 窗口内未见 0 过渡（更早开仓/历史超窗）→ 零值诚实缺省。
func (q *CEXPositionQuerier) binanceOpenedAt(ctx context.Context, symbol, side string) (time.Time, error) {
	params := "symbol=" + symbol + "&limit=1000&timestamp=" + strconv.FormatInt(time.Now().UnixMilli(), 10) + "&recvWindow=5000"
	url := q.baseURL + "/v1/userTrades?" + params + "&signature=" + binanceSign(q.apiSecret, params)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return time.Time{}, err
	}
	req.Header.Set("X-MBX-APIKEY", q.apiKey)
	resp, err := q.client.Do(req)
	if err != nil {
		return time.Time{}, fmt.Errorf("binance userTrades: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return time.Time{}, err
	}
	if resp.StatusCode != 200 {
		return time.Time{}, fmt.Errorf("binance userTrades: http %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var trades []struct {
		Side         string `json:"side"`
		PositionSide string `json:"positionSide"`
		Qty          string `json:"qty"`
		Time         int64  `json:"time"`
	}
	if err := json.Unmarshal(data, &trades); err != nil {
		return time.Time{}, fmt.Errorf("binance userTrades json: %w", err)
	}
	sort.Slice(trades, func(i, j int) bool { return trades[i].Time < trades[j].Time })
	type bucket struct {
		amt      float64
		lastOpen int64
	}
	buckets := map[string]*bucket{}
	for _, tr := range trades {
		qty, perr := f64z(tr.Qty)
		if perr != nil {
			return time.Time{}, fmt.Errorf("parse qty=%q: %w", tr.Qty, perr)
		}
		ps := tr.PositionSide
		if ps == "" {
			ps = "BOTH"
		}
		b := buckets[ps]
		if b == nil {
			b = &bucket{}
			buckets[ps] = b
		}
		delta := qty
		if (ps == "SHORT") == (tr.Side == "BUY") { // SHORT 桶 BUY=减仓；BOTH/LONG 桶 BUY=加仓
			delta = -delta
		}
		prev, cur := b.amt, b.amt+delta
		if math.Abs(prev) < 1e-9 && math.Abs(cur) >= 1e-9 {
			b.lastOpen = tr.Time // 0→非0 过渡=开仓点；最后一次胜出
		}
		b.amt = cur
	}
	// 选桶：对冲模式按持仓方向取 LONG/SHORT 精确桶；单向/缺桶回退 BOTH。
	pick := "LONG"
	if side == "SELL" {
		pick = "SHORT"
	}
	for _, ps := range []string{pick, "BOTH"} {
		if b := buckets[ps]; b != nil && b.lastOpen > 0 {
			return time.UnixMilli(b.lastOpen), nil
		}
	}
	return time.Time{}, nil
}
