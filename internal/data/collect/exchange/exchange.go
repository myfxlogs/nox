// Package exchange 实现 CEX 行情采集（§3.3）。
// funding 三所原生 WS 流为唯一 fact 源（D-509 流化、D-518 REST 兜底退役）；
// 本文件保留共享 helper（结算周期/年化口径）+ history 回填与 WS 适配共用基础件。
// 产出 Fact（funding）+ Quote（ticker），分别经 FactSink 和 QuoteBus。
package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Venue 常量。
const (
	VenueBinance = "binance"
	VenueGate    = "gate"
	VenueOKX     = "okx"
)

// fundingPeriodsPerDay 根据结算间隔（ms）计算每日结算次数。
// 4h=6, 1h=24, 8h=3。intervalMs ≤ 0 → 回退 3（8h，禁静默，调用方计数日志）。
//
// 修复：旧版用「nextFunding - now」（剩余时间）当间隔 → 结算前 1h 内剩余极短
// → periodsPerDay 爆炸到 26 → 年化虚高 8.7 倍（94.9% vs 真实 10.95%）。
// 正确做法：用「nextFunding - prevFunding」（结算间隔），与当前时刻无关。
func fundingPeriodsPerDay(intervalMs float64) int {
	if intervalMs <= 0 {
		return 3 // 回退 8h×3（禁静默，调用方计数日志）
	}
	periodsPerDay := float64(24*3600*1000) / intervalMs
	if periodsPerDay < 1 {
		return 3
	}
	return int(periodsPerDay)
}

// annualizeFunding 将单次 funding rate 转为年化百分比（D-077 修复⑪B N1）。
// 公式：rate × periodsPerDay × 365 × 100
func annualizeFunding(rate float64, periodsPerDay int) float64 {
	return rate * float64(periodsPerDay) * 365 * 100
}

// Config CEX 采集配置。
type Config struct {
	Symbols        []string       // 基础币种 BTC/ETH/TRX
	SymProvider    SymbolProvider // D-369 S2：动态符号集（radar_watchlist ∪ env；nil=仅 env）
	BinanceBaseURL string         // Binance fapi base，可指向 CF Worker 代理
	// BinanceSpotInfoURL D-551 S2：现货 exchangeInfo 端点（spot 订阅有效性过滤）；
	// 空=env NOX_BINANCE_SPOT_INFO_URL→默认 data-api.binance.vision 公开域。
	BinanceSpotInfoURL string
	// BinanceFuturesInfoURL perp exchangeInfo 端点（indiv perp 存在性闸目录源）；
	// 空=env NOX_BINANCE_FUTURES_INFO_URL→默认 fapi.binance.com 官方端点（D-127 P2
	// 预留：CN/受限部署可覆盖指 cex-proxy 域名避 451）。
	BinanceFuturesInfoURL string
	GateBaseURL           string
	OKXBaseURL            string
	Client                *http.Client
	// AlertFn 可选告警通道（D-507 重连风暴 fail-loud）；nil=静默跳过（测试默认）。
	AlertFn func(level, msg string)
	// DepthTimeout per-venue depth REST 每请求预算（egress 劣化带病工作面——
	// 慢 venue 有界不拖整批；app_config NOX_CEX_DEPTH_TIMEOUT_<VENUE> 秒级
	// 装配期读，缺省 8s；未配=回退共享 poll ctx 零行为变）。
	DepthTimeout map[string]time.Duration
	// ADLPositionHit D-101：ADL 风险帧持仓关联探针——入参=帧内 venue 原生合约号，
	// 返回=该 venue 我们的活跃仓（四态）OPEN 腿是否持有该合约（gate
	// futures.adl_warning 帧用：合约级市场广播→持仓关联过滤）。false 仅在确证
	// 非持有时返回；探针未接线/帧缺合约=关联不明，处理侧 fail-safe 视同命中
	// （宁多告不漏告——禁把真风险静默降档）。
	ADLPositionHit func(venueSymbol string) bool
}

// DefaultConfig 返回默认配置。
func DefaultConfig(symbols []string) Config {
	c := Config{
		Symbols:        symbols,
		BinanceBaseURL: "https://fapi.binance.com",
		GateBaseURL:    "https://api.gateio.ws",
		OKXBaseURL:     "https://www.okx.com",
		Client:         &http.Client{Timeout: 10 * time.Second},
		// 缺省 8s/venue 带病预算（装配期可被 app_config 键覆写——
		// NOX_CEX_DEPTH_TIMEOUT_<VENUE> 秒，业务配置唯一真相=PG 契约遵行）。
		DepthTimeout: map[string]time.Duration{
			"binance": 8 * time.Second, "gate": 8 * time.Second, "okx": 8 * time.Second,
		},
	}
	return c
}

// BinanceFunding/GateFunding/OKXFunding REST 轮询采集器已退役（D-518 S3：
// 三所 WS 原生流实证后删码；事实源=binance_ws_markprice/okx_ws_v5/gate_ws_futures）。

func getJSON(ctx context.Context, client *http.Client, url string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}
