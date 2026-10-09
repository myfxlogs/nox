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
	"strconv"
	"strings"
	"time"

	v1 "arb/internal/gen/dashboardv1"
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

	// 权益估算：Σ (free+locked) × USDT 价；USDT 价=1，其余取公开 ticker/price（失败该币跳过，诚实估算）。
	// D-364 v2 S1：非 USDT 标资产不读取不统计（业主明谕「其他的币种都不用读取和统计」
	// ——USDT=CEX 本位币对标 CFD USD）——循环首行过滤，非 USDT 标不进 total/holdings/
	// 不发 ticker 定价请求（~90 个 LD* 杂币每轮省 ~90 次 ticker 调用）。
	// D-361 LD 计价链（binanceUsdtPrice 兜底序）随「不读取」不可达，整段删除（史实在 git）。
	total := 0.0
	freeTotal := 0.0 // D-542：free_margin=Σ USDT 标 free（弹窗实拉校准+快照列真值）
	var holdings []*v1.Holding
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
		if b.Asset != "USDT" && strings.TrimPrefix(b.Asset, "LD") != "USDT" {
			continue // 非 USDT 标（LDUSDC/LDTRX/LDO/BFUSD/杂币）：不读取不统计
		}
		// USDT/LDUSDT 皆 1:1 计 qty，无需价
		total += qty
		freeTotal += free
		holdings = append(holdings, &v1.Holding{Ccy: b.Asset, Qty: qty, UsdtValue: qty})
	}
	return &AccountInfo{
		VenueKey: q.VenueKey(), Venue: q.venue, VenueType: q.venueType, AssetType: "cex",
		Balance: total, Equity: total, FreeMargin: freeTotal, Currency: "USDT",
		Simulated: q.simulated, Website: q.website, Holdings: holdings,
	}, nil
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
	// D-405 S2：buffer_multiple=marginBalance/maintMargin（写时归一 equity/MMR；
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
	// D-542：availableBalance 是 venue 权威可用（wb−占用保证金−冻结，含全仓）——
	// 缺失/解析失败按 equity 回退（不猜负值）。
	avail := mb
	if ab, perr := f64z(acc.AvailableBalance); perr == nil && ab > 0 {
		avail = ab
	}
	// D-142：equity=totalMarginBalance（钱包余额+未实现盈亏，与 WS wb+ΣUP 同口径）；
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
