// D-271 S5：CEX 只读扫描器装配（从 exchange_configs 读凭据，构造 Scanner）。
package main

import (
	"context"
	"log"
	"nox/internal/data/bus"

	"nox/internal/api/dashboard"
	"nox/internal/data/cexscan"
	"nox/internal/data/collect/exchange"
	"nox/internal/platform/config"
	"nox/internal/platform/httpx"
	"nox/internal/platform/safego"
	"nox/internal/store/pgstore"
)

// defaultCexSymbols 采集基集（NOX_CEX_SYMBOLS 缺省）：D-300 S2+D-303 +XAU+D-308 +十符号；
// D-420 S1 +SPX——三所 SPX 家族基名=SPX（SPXUSDT/SPX-USDT-SWAP/SPX_USDT）而既有列表只有
// SPX500（gate 独有）→ binance/okx SPX funding 静默缺，补三所对称。
var defaultCexSymbols = []string{"BTC", "ETH", "TRX", "PAXG", "XAUT", "XAU", "COPPER", "XAG", "CL", "BZ", "XPT", "XPD", "NAS100", "US30", "SPX500", "SPX", "JP225"}

// buildCexExchangeConfig 装配 CEX 行情采集配置（D-451 S1：main() 提取，纯搬家零语义）。
// 返回 cexSymbols 随行（main 下游 startIncomeCollector/startTickSampler 仍用）。
func buildCexExchangeConfig(ctx context.Context, st *pgstore.Store, configKick *bus.Topic[struct{}], cfg *config.Config, secRes *config.SecretResolver) (exchange.Config, []string) {
	// D-077 修复⑪B N8：cexSymbols env 化（NOX_CEX_SYMBOLS，默认 BTC,ETH,TRX，逗号分隔）
	cexSymbols := cfg.CexSymbols
	if len(cexSymbols) == 0 {
		cexSymbols = defaultCexSymbols // D-300 S2 起历次扩容见 defaultCexSymbols 注释
	}

	// D-145 env 收口：WS/HTTP 端点+token 注入先于一切采集构造（cfg=DB>env>default 产物）。
	exchange.SetProxyEndpoints(exchange.ProxyEndpoints{
		CexWSBase: cfg.CexWSBase, CexWSBaseBackup: cfg.CexWSBaseBackup,
		BinanceProxyBackup: cfg.BinanceProxyURLBackup,
		PerpPublicWSURL:    cfg.BinancePerpPublicWSURL, PerpMarketWSURL: cfg.BinancePerpMarketWSURL,
		SpotWSURL: cfg.BinanceSpotWSURL, GateWSMode: cfg.GateWSMode,
	})
	httpx.Configure(cfg.BinanceProxyCA, secRes.Get("NOX_BINANCE_PROXY_TOKEN"), [][2]string{
		{cfg.BinanceProxyURL, cfg.BinanceProxyURLBackup},
		{cfg.CexWSBase, cfg.CexWSBaseBackup},
		{cfg.OKXProxyURL, cfg.CexWSBaseBackup},
	})
	cexCfg := exchange.DefaultConfig(cexSymbols)
	// D-369 S2：radar_watchlist 符号集供给（binance 每 tick 重读；gate/okx WS 差集增量订阅）
	watchlistKick, _ := configKick.Subscribe()
	cexCfg.SymProvider = newWatchlistSymbolProvider(ctx, st, watchlistKick) // D-369 S2/R1+D-469b S5：kick 主+5min 兜底
	if cfg.BinanceProxyURL != "" {
		cexCfg.BinanceBaseURL = cfg.BinanceProxyURL
	}
	// TRADIFI 采集轴（D-624 前置闸②）：exchangeInfo 周期枚举 TRADIFI_PERPETUAL
	// 品种→SymProvider 并源——funding/markPrice 既有采集轴自动扩面（禁双写径）。
	tradifi := exchange.NewTradifiSymbols(httpx.BinanceApiSegBase(cexCfg.BinanceBaseURL, "fapi"))
	tradifi.SetClient(httpx.Client())
	safego.GoLoop("main:tradifi-syms", func() { tradifi.StartTradifiRefresh(ctx) })
	cexCfg.SymProvider = exchange.MultiSymbolProvider{cexCfg.SymProvider, tradifi}
	// D-551 S2：现货 exchangeInfo 端点注入（空=采集器内默认 vision 公开域）。
	cexCfg.BinanceSpotInfoURL = cfg.BinanceSpotInfoURL
	if cfg.OKXProxyURL != "" {
		cexCfg.OKXBaseURL = cfg.OKXProxyURL
	}
	dashboard.SetBinanceProxyBase(cfg.BinanceProxyURL) // D-145：dashboard 缺省 base 回退同径
	cexCfg.Client = httpx.Client()                     // CA 信任 + X-Proxy-Token 注入（D-055 静态代理）
	return cexCfg, cexSymbols
}

// buildCexScanner 从 exchange_configs 装配 CEX 只读扫描器。
func buildCexScanner(cfg *config.Config, store *pgstore.Store, am *dashboard.AccountMonitor, refreshTopic *bus.Topic[string]) (*cexscan.Scanner, error) {
	exchs, err := store.ListExchangeConfigs(context.Background())
	if err != nil {
		return nil, err
	}
	sc := cexscan.NewScanner(cfg, store, am, exchs)
	sc.SetRefreshTopic(refreshTopic) // D-007 S3：scanOnce 写毕→cex_scan emit
	return sc, nil
}

// startCexScanner 启动 CEX 只读扫描器（nil 安全）。
func startCexScanner(s *cexscan.Scanner, ctx context.Context, cfg *config.Config) {
	if s == nil {
		bus.MarkPermProbeBootDone() // D-193③：无扫描器=权限面无从就绪，即标 done 防 oppmatch 死等
		bus.MarkDCIBootDone()       // D-198：无扫描器=DCI 面无从就绪，同标防续执死等
		return
	}
	safego.GoLoop("main:cexscan", func() {
		s.Run(ctx)
	})
	log.Printf("[wiring] cexscan service started (interval=%s symbols=%v)", cfg.CexScanInterval, cfg.CexScanSymbols)
}

// kickChanSend D-469a S4：kick 通道非阻塞 send（sched.Kick 同型合并语义——
// 已有待发即丢，消费端收到即全量回读）。
func kickChanSend(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}
