// cex_positions_binance.go — Binance perp 持仓查询（D-121；cex_positions.go 拆出，D-508 S2b 行数归位）。
package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// === Binance ===
// GET /fapi/v2/positionRisk — 全部 USDT-M 合约持仓（含 0 仓位）。positionAmt>0 多/<0 空；unRealizedProfit=浮盈。
// D-146 S5：positionRisk 无 initialMargin/maintMargin（账户级字段）——逐仓→isolatedWallet、
// 全仓→|notional|/leverage（D-343：空头 notional 负值取绝对值）；MaintMargin=0 走 D-124 账户分摊。
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
		Notional         string `json:"notional"`       // 名义价值（USDT，空头为负；全仓保证金=|notional|/leverage）
	}
	if err := json.Unmarshal(data, &positions); err != nil {
		return nil, fmt.Errorf("binance positions json: %w", err)
	}
	var out []VenueOrder
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
		// D-146 S5 微件③ 返修：逐仓→isolatedWallet；全仓→notional/leverage
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
		out = append(out, VenueOrder{
			Ticket: p.Symbol, Symbol: p.Symbol, Side: side, Volume: vol,
			OpenPrice: entryPx, Venue: q.venue, VenueKey: q.VenueKey(), Profit: upl,
			InitialMargin: initMargin, // MaintMargin=0：positionRisk 无此字段，走 D-124 账户分摊
			PosSide:       posSide,    // D-195-C2：从 amt 正负推导
		})
	}
	return out, nil
}
