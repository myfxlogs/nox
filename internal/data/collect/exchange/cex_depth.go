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
	"sync"
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
	for _, venue := range []string{VenueBinance, VenueGate, VenueOKX} {
		out = append(out, FetchCexDepthsVenue(ctx, cfg, venue, bases)...)
	}
	return out
}

// FetchCexDepthsVenue D-280 方案 A：单所×基名集顶档采样——分所精集口径
// （每所基名集独立，候选腿真实集合天然自对齐；失败符号跳过同惯例）。
func FetchCexDepthsVenue(ctx context.Context, cfg Config, venue string, bases []string) []DepthQuote {
	var out []DepthQuote
	for _, base := range bases {
		dq, err := fetchDepth(ctx, cfg, venue, base)
		if err != nil {
			log.Printf("[exchange:depth] %s %s: %v (skip)", venue, base, err)
			continue
		}
		out = append(out, dq)
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
			Bids []gateOBEntry `json:"bids"`
			Asks []gateOBEntry `json:"asks"`
		}
		if err := getJSONInto(ctx, cfg.Client, cfg.GateBaseURL+"/api/v4/futures/usdt/order_book?contract="+sym+"&limit=5", &body); err != nil {
			return DepthQuote{}, err
		}
		mult, err := gateContractMult(ctx, cfg, sym)
		if err != nil {
			return DepthQuote{}, err
		}
		return depthRow(VenueGate, sym, gatePairs(body.Bids, mult), gatePairs(body.Asks, mult))
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

// gateOBEntry gate 期货盘口条目——对象组 {p:价,s:张数}（与 binance/okx 的
// [价,量] 字符串数组异形；D-112 实拍：旧 [][]string 解析使 gate 深度自 D-420 起全灭）。
type gateOBEntry struct {
	P string  `json:"p"`
	S float64 `json:"s"` // 张数（非 base 量；负值=平仓向单）
}

// gatePairs gate 对象条目→depthRow 通用 [价, base量] 对：张数×quanto_multiplier 折 base。
func gatePairs(rows []gateOBEntry, mult float64) [][]string {
	out := make([][]string, len(rows))
	for i, r := range rows {
		out[i] = []string{r.P, strconv.FormatFloat(math.Abs(r.S)*mult, 'g', -1, 64)}
	}
	return out
}

// gateQuantoCache 合约乘数进程级缓存（quasi-static；错误不入缓存、下一轮重试）。
var gateQuantoCache sync.Map // contract → float64

// gateContractMult 取 gate 合约 quanto_multiplier（张→base 折算率；读失败 fail-closed
// 该 symbol 本轮无深度——与「脏值上抛」同律）。
func gateContractMult(ctx context.Context, cfg Config, sym string) (float64, error) {
	if v, ok := gateQuantoCache.Load(sym); ok {
		return v.(float64), nil
	}
	var body struct {
		QuantoMultiplier string `json:"quanto_multiplier"`
	}
	if err := getJSONInto(ctx, cfg.Client, cfg.GateBaseURL+"/api/v4/futures/usdt/contracts/"+sym, &body); err != nil {
		return 0, fmt.Errorf("gate %s contract spec: %w", sym, err)
	}
	m, err := strconv.ParseFloat(body.QuantoMultiplier, 64)
	if err != nil || m <= 0 {
		return 0, fmt.Errorf("gate %s quanto_multiplier 无效 %q", sym, body.QuantoMultiplier)
	}
	gateQuantoCache.Store(sym, m)
	return m, nil
}

// parseNum 数字字符串解析（三所盘口条目均为 [price,size,...] 字符串数组；
// 脏值上抛——盘口脏条目=该 symbol 本轮无深度，不进报价面）。
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
