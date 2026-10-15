// cex_account_binance.go D-273 拆分：Binance spot/perp 账户查询 + ticker 估价 + 签名
// （自 cex_account.go 纯搬家，零逻辑变化；check-lines ≤300）。
package reconcile

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	v1 "nox/internal/gen/dashboardv1"
)

// === Binance ===

func (q *CEXAccountQuerier) accountBinance(ctx context.Context) (*AccountInfo, error) {
	if q.venueType == "perp" {
		return q.accountBinancePerp(ctx)
	}
	// spot base：BINANCE_PROXY_URL 是 fapi 风格（采集同法 replace fapi→api 得 spot）。
	spotBase := strings.Replace(q.baseURL, "fapi", "api", 1)
	params := url.Values{}
	params.Set("timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))
	params.Set("recvWindow", "5000")
	sig := binanceSign(q.apiSecret, params.Encode())
	params.Set("signature", sig)

	h := http.Header{}
	h.Set("X-MBX-APIKEY", q.apiKey) // 令牌由 httpx 传输层自动注入
	body, err := q.get(ctx, spotBase+"/v3/account?"+params.Encode(), h)
	if err != nil {
		return nil, fmt.Errorf("binance account: %w", err)
	}
	var acc struct {
		Balances []struct {
			Asset  string `json:"asset"`
			Free   string `json:"free"`
			Locked string `json:"locked"`
		} `json:"balances"`
	}
	if err := json.Unmarshal(body, &acc); err != nil {
		return nil, fmt.Errorf("binance account json: %w", err)
	}

	// D-528：非稳定币现货计价入 NAV——「非 USDT 不读取」旧口径推翻（BTC 现货
	// ~$1034 全账本不可见实证）。分币持仓全枚举原生量落快照分币行（D-285 零换算），
	// 聚合 Equity 仍 USDT 估算展示（公开 ticker/price 全符号单读，读败 fail-closed）。
	// LD* 前缀=earn 持仓现货镜像（simple-earn 申购仓位回显）——earn 池行已独立记账，
	// spot 域整体排除（free/equity 皆不计；计则与 binance:earn 行双计 NAV——
	// LDUSDT=1088.8 假 free 致 autoexec #502 venue 真拒单实证）。
	// 注意「真 LD* 代币」白名单：LDO=Lido DAO 真资产非 earn 回显（其 earn 镜像应
	// 为 LDLDO）——误排则现货 LDO 持仓凭空消失。
	freeMargin := 0.0 // free_margin=裸 USDT free（exec 可动读源口径；LD* 不入）
	type bal struct{ free, qty float64 }
	per := map[string]bal{}
	for _, b := range acc.Balances {
		free, perr := f64z(b.Free)
		if perr != nil {
			return nil, fmt.Errorf("parse %s=%q: %w", "b.Free", b.Free, perr)
		}
		locked, perr := f64z(b.Locked)
		if perr != nil {
			return nil, fmt.Errorf("parse %s=%q: %w", "b.Locked", b.Locked, perr)
		}
		qty := free + locked
		if qty <= 0 {
			continue
		}
		if isBinanceLDEcho(b.Asset) {
			continue // earn 镜像排除（注释头）
		}
		per[b.Asset] = bal{free: free, qty: qty}
	}
	if f, ok := per["USDT"]; ok {
		freeMargin = f.free
	}
	// 计价面：稳定币集 1:1，其余取 /v3/ticker/price 全符号表（单读非逐币）。
	// 尘埃阈 <$1 滤行（雪崩杂币零头防快照行洪）；无价币（无 XXXUSDT 对）不滤
	// 不估价——如实留行 UsdtValue=0。
	total := 0.0
	usdVal := map[string]float64{}
	var priced bool
	for asset, b := range per {
		px, ok := binanceSpotUSDPrice(asset, nil)
		if !ok {
			if !priced {
				pxm, err := q.binanceSpotPrices(ctx, spotBase)
				if err != nil {
					return nil, err
				}
				usdVal = pxm
				priced = true
			}
			px, ok = binanceSpotUSDPrice(asset, usdVal)
		}
		if ok && b.qty*px < spotDustUSD && asset != "USDT" {
			delete(per, asset) // 尘埃行滤除（<1 USD 等价；USDT=本位锚行恒留）
			continue
		}
		if ok {
			total += b.qty * px
		}
	}
	var holdings []*v1.Holding
	freeByCcy := map[string]float64{}
	ccys := make([]string, 0, len(per))
	for c := range per {
		ccys = append(ccys, c)
	}
	sort.Strings(ccys)
	for _, c := range ccys {
		b := per[c]
		px, _ := binanceSpotUSDPrice(c, usdVal)
		holdings = append(holdings, &v1.Holding{Ccy: c, Qty: b.qty, UsdtValue: b.qty * px})
		freeByCcy[c] = b.free
	}
	return &AccountInfo{
		VenueKey: q.VenueKey(), Venue: q.venue, VenueType: q.venueType, AssetType: "cex",
		Balance: total, Equity: total, FreeMargin: freeMargin, Currency: "USDT",
		Simulated: q.simulated, Website: q.website, Holdings: holdings,
		FreeQtyByCcy: freeByCcy,
	}, nil
}

// spotDustUSD 现货尘埃阈（USD 等价）——低于此值的零头币不进持仓分币行。
const spotDustUSD = 1.0

// binanceStablePx USD 族稳定币 1:1 计价集（无 XXXUSDT 对的 USD 锚资产同域——
// BFUSD 保证金币等归入；缺对 ≠ 编价，USD 族属同记帐单位非编）。
var binanceStablePx = map[string]bool{
	"USD": true, "USDT": true, "USDC": true, "BUSD": true, "FDUSD": true,
	"DAI": true, "TUSD": true, "USDP": true, "USD1": true, "BFUSD": true,
}

// binanceSpotUSDPrice 资产→USD 价：稳定币集 1:1；余者查 pxm["<A>USDT"]；
// 无价/未采 → ok=false（不编价）。
func binanceSpotUSDPrice(asset string, pxm map[string]float64) (float64, bool) {
	if binanceStablePx[asset] {
		return 1, true
	}
	if pxm != nil {
		if p, ok := pxm[asset+"USDT"]; ok && p > 0 {
			return p, true
		}
	}
	return 0, false
}

// binanceSpotPrices /v3/ticker/price 全符号表（公开端点免签名单读）→
// symbol→price map。读败/解析败 fail-closed 返错（不编价不静默）。
func (q *CEXAccountQuerier) binanceSpotPrices(ctx context.Context, spotBase string) (map[string]float64, error) {
	body, err := q.get(ctx, spotBase+"/v3/ticker/price", http.Header{})
	if err != nil {
		return nil, fmt.Errorf("binance ticker/price: %w", err)
	}
	var rows []struct {
		Symbol string `json:"symbol"`
		Price  string `json:"price"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		// 单符号形态（{"symbol":..,"price":..}）兼容——部分镜像回对象非数组
		var one struct {
			Symbol string `json:"symbol"`
			Price  string `json:"price"`
		}
		if err2 := json.Unmarshal(body, &one); err2 != nil || one.Symbol == "" {
			return nil, fmt.Errorf("binance ticker/price json: %w", err)
		}
		rows = append(rows, struct {
			Symbol string `json:"symbol"`
			Price  string `json:"price"`
		}{Symbol: one.Symbol, Price: one.Price})
	}
	m := map[string]float64{}
	for _, r := range rows {
		p, perr := strconv.ParseFloat(r.Price, 64)
		if perr == nil && p > 0 {
			m[r.Symbol] = p
		}
	}
	return m, nil
}

// isBinanceLDEcho LD* earn 镜像判定：LD 前缀+尾段非空=earn 持仓回显；
// 白名单「真 LD* 代币」（LDO=Lido DAO——其 earn 镜像为 LDLDO）不排。
func isBinanceLDEcho(asset string) bool {
	if asset == "LDO" {
		return false
	}
	return strings.HasPrefix(asset, "LD") && len(asset) > 2
}

// accountBinancePerp 合约账户权益：GET /fapi/v2/account，totalWalletBalance 为权威 USDT 权益
// （D-059 实施修正② 复核实测：模拟盘合约曾在独立子域 demo-fapi.binance.com——
// demo 支路 D-516 收编删除，baseURL 由调用方给到 fapi 前缀，此处直接拼 /fapi 路径）。
func (q *CEXAccountQuerier) accountBinancePerp(ctx context.Context) (*AccountInfo, error) {
	params := url.Values{}
	params.Set("timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))
	params.Set("recvWindow", "5000")
	sig := binanceSign(q.apiSecret, params.Encode())
	params.Set("signature", sig)

	h := http.Header{}
	h.Set("X-MBX-APIKEY", q.apiKey) // 令牌由 httpx 传输层自动注入
	body, err := q.get(ctx, q.baseURL+"/v2/account?"+params.Encode(), h)
	if err != nil {
		return nil, fmt.Errorf("binance perp account: %w", err)
	}
	var acc struct {
		TotalWalletBalance string `json:"totalWalletBalance"`
		MarginBalance      string `json:"totalMarginBalance"` // D-405 S2：钱包余额+未实现盈亏（equity）
		MaintMargin        string `json:"totalMaintMargin"`   // D-405 S2：账户级维持保证金（MMR）
		AvailableBalance   string `json:"availableBalance"`   // D-542：venue 权威可用余额（free_margin 真源）
	}
	if err := json.Unmarshal(body, &acc); err != nil {
		return nil, fmt.Errorf("binance perp account json: %w", err)
	}
	wb, perr := f64z(acc.TotalWalletBalance)
	if perr != nil {
		return nil, fmt.Errorf("parse %s=%q: %w", "acc.TotalWalletBalance", acc.TotalWalletBalance, perr)
	}
	// buffer_multiple=marginBalance/maintMargin（写时归一 equity/MMR；
	// maintMargin<=0 无仓 → 不采 0=未采语义，marginGuard 按 data_stale 处理需区分——
	// 空仓账户 MMR=0 实际缓冲无穷大，此处落 0 由 guard 判「无仓恒 normal」）。
	var buffer float64
	mb, perr := f64z(acc.MarginBalance)
	if perr != nil {
		return nil, fmt.Errorf("parse %s=%q: %w", "acc.MarginBalance", acc.MarginBalance, perr)
	}
	if mb > 0 {
		mm, perr := f64z(acc.MaintMargin)
		if perr != nil {
			return nil, fmt.Errorf("parse %s=%q: %w", "acc.MaintMargin", acc.MaintMargin, perr)
		}
		if mm > 0 {
			buffer = mb / mm
		}
	}
	// availableBalance 是 venue 权威可用（wb−占用保证金−冻结，含全仓）——
	// 缺失/解析失败按 equity 回退（不猜负值）。
	avail := mb
	if ab, perr := f64z(acc.AvailableBalance); perr == nil && ab > 0 {
		avail = ab
	}
	// equity=totalMarginBalance（钱包余额+未实现盈亏，与 WS wb+ΣUP 同口径）；
	// Balance 保留 totalWalletBalance（权益−未实现盈亏的净额，展示域分列）。
	return &AccountInfo{
		VenueKey: q.VenueKey(), Venue: q.venue, VenueType: q.venueType, AssetType: "cex",
		Balance: wb, Equity: mb, FreeMargin: avail, Currency: "USDT",
		Simulated: q.simulated, Website: q.website, BufferMultiple: buffer,
	}, nil
}

// binanceSign HMAC-SHA256 hex 签名（与 exec.CEXExecutor 同法）。
func binanceSign(secret, query string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(query))
	return hex.EncodeToString(mac.Sum(nil))
}
