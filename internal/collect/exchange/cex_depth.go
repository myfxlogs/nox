// cex_depth.go —— D-420 S2：盘口顶档深度采样（公开端点无凭据，白名单符号）。
// 语义：bid1_usd/ask1_usd=顶档买/卖价（USD 计价），bid1_qty/ask1_qty=顶档量（base），
// MidDepthUSD=顶档中间价×可成交量（min(bid,ask)）——容量与滑点研究的数据前提（v1 顶档够定性）。
// 取最优档=max bid / min ask（对所内排序序不敏感）；venue 无此符号=跳过（fail-open，
// 白名单符号在不在所由所定，与 funding 采集「所间不对称缺符号静默」同惯例）。
package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"
)

// DepthWhitelistBases D-420 S2：深度采样白名单基名（SSOT §1 S2：SPX500/XPT/XPD/XAU/CLU；
// CLU=油族 SSOT 记法，三所实际基名=CL——binance CLUSDT/gate CL_USDT/okx CL-USDT-SWAP）。
var DepthWhitelistBases = []string{"SPX500", "XPT", "XPD", "XAU", "CL"}

// DepthQuote 单 venue×symbol 顶档快照。
type DepthQuote struct {
	Venue  string
	Symbol string
	Bid1   float64
	Ask1   float64
	BidQty float64
	AskQty float64
}

// MidDepthUSD 顶档中间价深度（USD 名义）=mid×min(bid_qty, ask_qty)。
func (d DepthQuote) MidDepthUSD() float64 {
	mid := (d.Bid1 + d.Ask1) / 2
	return mid * math.Min(d.BidQty, d.AskQty)
}

// FetchCexDepths 白名单基名×三所顶档采样（单 venue×symbol 失败跳过）。
func FetchCexDepths(ctx context.Context, cfg Config, bases []string) []DepthQuote {
	var out []DepthQuote
	for _, base := range bases {
		for _, venue := range []string{VenueBinance, VenueGate, VenueOKX} {
			dq, err := fetchDepth(ctx, cfg, venue, base)
			if err != nil {
				log.Printf("[exchange:depth] %s %s: %v (skip)", venue, base, err)
				continue
			}
			out = append(out, dq)
		}
	}
	return out
}

// fetchDepth 单 venue×base 顶档（各所 instrument 命名差异在构造处收口）。
func fetchDepth(ctx context.Context, cfg Config, venue, base string) (DepthQuote, error) {
	switch venue {
	case VenueBinance:
		sym := base + "USDT"
		var body struct {
			Bids [][]string `json:"bids"`
			Asks [][]string `json:"asks"`
		}
		if err := getJSONInto(ctx, cfg.Client, cfg.BinanceBaseURL+"/fapi/v1/depth?symbol="+sym+"&limit=5", &body); err != nil {
			return DepthQuote{}, err
		}
		return depthRow(VenueBinance, sym, body.Bids, body.Asks)
	case VenueGate:
		sym := base + "_USDT"
		var body struct {
			Bids [][]string `json:"bids"`
			Asks [][]string `json:"asks"`
		}
		if err := getJSONInto(ctx, cfg.Client, cfg.GateBaseURL+"/api/v4/futures/usdt/order_book?contract="+sym+"&limit=5", &body); err != nil {
			return DepthQuote{}, err
		}
		return depthRow(VenueGate, sym, body.Bids, body.Asks)
	case VenueOKX:
		sym := base + "-USDT-SWAP"
		var body struct {
			Data []struct {
				Bids [][]string `json:"bids"`
				Asks [][]string `json:"asks"`
			} `json:"data"`
		}
		if err := getJSONInto(ctx, cfg.Client, cfg.OKXBaseURL+"/api/v5/market/books?instId="+sym+"&sz=5", &body); err != nil {
			return DepthQuote{}, err
		}
		if len(body.Data) == 0 {
			return DepthQuote{}, fmt.Errorf("empty books")
		}
		return depthRow(VenueOKX, sym, body.Data[0].Bids, body.Data[0].Asks)
	}
	return DepthQuote{}, fmt.Errorf("unknown venue %q", venue)
}

// parseNum 数字字符串解析（三所盘口条目均为 [price,size,...] 字符串数组；
// D-508 S2b：脏值上抛——盘口脏条目=该 symbol 本轮无深度，不进报价面）。
func parseNum(parts []string) (float64, error) {
	if len(parts) == 0 || parts[0] == "" {
		return 0, nil
	}
	return strconv.ParseFloat(parts[0], 64)
}

// getJSONInto GET→JSON 解析进 dst（深度端点形状各所异构，类型化解析）。
func getJSONInto(ctx context.Context, client *http.Client, url string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}

// depthRow 取最优档（max bid / min ask——对所内排序序不敏感）；空侧=错误。
func depthRow(venue, symbol string, bids, asks [][]string) (DepthQuote, error) {
	dq := DepthQuote{Venue: venue, Symbol: symbol}
	pnum := func(parts []string, idx int) (float64, error) {
		if len(parts) <= idx {
			return 0, nil
		}
		return parseNum(parts[idx:])
	}
	for _, b := range bids {
		p, perr := pnum(b, 0)
		if perr != nil {
			return DepthQuote{}, fmt.Errorf("depth bid %v: %w", b, perr)
		}
		q, qerr := pnum(b, 1)
		if qerr != nil {
			return DepthQuote{}, fmt.Errorf("depth bid qty %v: %w", b, qerr)
		}
		if p > dq.Bid1 {
			dq.Bid1, dq.BidQty = p, q
		}
	}
	for _, a := range asks {
		p, perr := pnum(a, 0)
		if perr != nil {
			return DepthQuote{}, fmt.Errorf("depth ask %v: %w", a, perr)
		}
		q, qerr := pnum(a, 1)
		if qerr != nil {
			return DepthQuote{}, fmt.Errorf("depth ask qty %v: %w", a, qerr)
		}
		if dq.Ask1 == 0 || p < dq.Ask1 {
			dq.Ask1, dq.AskQty = p, q
		}
	}
	if dq.Bid1 <= 0 || dq.Ask1 <= 0 {
		return DepthQuote{}, fmt.Errorf("one-sided book (bid=%v ask=%v)", dq.Bid1, dq.Ask1)
	}
	return dq, nil
}
