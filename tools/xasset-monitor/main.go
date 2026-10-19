// PAXG-XAU 跨市场套利监控脚本（独立工具，cron 调度）。
// 9-16 业主并存裁定：PAXG+XAUT 两对都监控（XAUTUSDT=Tether Gold 永续，funding 当前更高）。
//
// 流程：拉 Binance 黄金代币合约 bid/ask + funding rate（走 cex-proxy，httpx.Client 自动注入 CA+token）
// → 读 Exness XAU tick 最新行 → 算价差/净收益/跟踪误差 → 入库 fin_xasset_monitor
// → 超阈值飞书告警。不自动下单，只监控+纸面交易支撑。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"strings"

	"nox/internal/data/keydomain"
	"nox/internal/platform/httpx"
	"nox/internal/store/pgstore"
)

// configureHttpxProxy D-145：httpx.Client 代理 CA+token 注入——唯一权威=
// app_config NOX_BINANCE_PROXY_CA + app_secrets NOX_BINANCE_PROXY_TOKEN（env 读径封死；
// 读败→空值注入降级运行，代理调用失败处自现错）。
func configureHttpxProxy(ctx context.Context, st *pgstore.Store) {
	var caPath, token string
	if rows, err := st.LoadAppConfig(ctx); err == nil {
		caPath = rows["NOX_BINANCE_PROXY_CA"]
	}
	if secrets, err := st.LoadAppSecrets(ctx); err == nil {
		token = secrets["NOX_BINANCE_PROXY_TOKEN"]
	}
	httpx.Configure(caPath, token, nil)
}

func main() {
	ctx := context.Background()
	backfill := flag.Bool("backfill", false, "D-341 S5：价差史深回填（ticks×klines 5min 对齐，src=backfill）")
	flag.Parse()
	if *backfill {
		dsn := os.Getenv("DB_DSN") // D-145 引导件：连库前必需
		if err := runBackfillMode(ctx, dsn); err != nil {
			log.Fatalf("xasset-monitor backfill: %v", err)
		}
		return
	}
	if err := run(ctx); err != nil {
		log.Fatalf("xasset-monitor: %v", err)
	}
}

func run(ctx context.Context) (err error) {
	dsn := os.Getenv("DB_DSN")
	if dsn == "" {
		return fmt.Errorf("DB_DSN 未设置")
	}
	st, err := pgstore.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect pg: %w", err)
	}
	defer st.Close()
	configureHttpxProxy(ctx, st) // D-145：代理 CA+token 注入（app_config/app_secrets 唯一权威）
	// named-return defer 单点捕获——启动/执行致命错统一进 reportFatal
	// （alerts critical + app_secrets 飞书直推 + 30min 去重；pair skip 不进此通道）。
	defer func() { reportFatal(ctx, st, err) }()

	// S5→D-144：监控簿三源派生（白名单∪universe 达标行∪active 纸面仓）×
	// ticks 在流 MT 账户动态枚举——NOX_XASSET_EXNESS_VENUE env 删除（钉死单账户
	// 致跨账户借用失真/新 broker 零覆盖，实证见 STATE D-144 行）。
	combos, uni, demand, accts, equiv, err := loadDerivationInputs(ctx, st)
	if err != nil {
		return fmt.Errorf("monitor pairs inputs: %w", err)
	}
	pairs, uncovered := deriveMonitorPairs(combos, uni, demand, accts, equiv)
	for _, u := range uncovered {
		log.Printf("[monitor-pairs] warn: 组合未被簿覆盖（一次性列名，B-008/D-144）: %s", u)
	}
	coverageAudit(ctx, st, pairs) // D-625 P1：覆盖账本自盯（两型 warn；在审键=本拍派生活键）
	return runPairs(ctx, st, pairs)
}

// runPairs S2：逐对取各自 exSym 报价（无新鲜 tick → 该对 skip+warn 逐对 fail-open，
// 9-16 业主并存裁定）——禁止任何腿顶替、禁止跨账户借用（D-144：各账户独立采样，
// p.mtVenueKey 携带该对所属账户）。返回首个 pair 失败错误（skip 非失败）。
// mt_venue 落库键=venue_key 形（"15032313:mt5"）；按 cexVenue 路由报价源。
func runPairs(ctx context.Context, st *pgstore.Store, pairs []monitorPair) error {
	var firstErr error
	var stills []string // D-160：簿面静止 pair 汇总（当轮至多一条 warn）
	for _, p := range pairs {
		mtBare := keydomain.BareOf(p.mtVenueKey)
		exBid, exAsk, err := latestExnessTick(ctx, st.Pool(), mtBare, p.exSym)
		if err != nil {
			log.Printf("xasset-monitor: pair=%s mt=%s skip (%s 腿 %s 无新鲜 tick: %v)", p.pair, p.mtVenueKey, mtBare, p.exSym, err)
			continue // S2：逐对 skip，禁止顶替腿
		}
		if err := runPair(ctx, st, p, p.mtVenueKey, exBid, exAsk); err != nil {
			// stale 命中二次判定——venue 活性锚成立=簿面静止降 warn
			// （stale 价不落簿=D-591 指标纯洁不动）；锚失效/超帽/无行=dead 走原链。
			var sde *staleDataErr
			if errors.As(err, &sde) && classifyStale(ctx, st.Pool(), sde) == "still" {
				log.Printf("xasset-monitor: pair=%s 簿面静止（venue 活性锚成立，降 warn）: %v", p.pair, err)
				stills = append(stills, fmt.Sprintf("%s %s", p.pair, sde))
				continue
			}
			log.Printf("xasset-monitor: pair=%s 失败: %v", p.pair, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	// D-160④ 聚合语义：任一 dead→critical 照旧（firstErr→reportFatal）；
	// 全 still→当轮至多一条 warn 汇总落行（30min 同键去重不刷屏、不推飞书）。
	if firstErr == nil && len(stills) > 0 {
		msg := "xasset-monitor 簿面静止（venue 活性锚成立）: " + strings.Join(stills, "; ")
		if !hasRecentSameKeyAlert(ctx, st, "warn", msg) {
			if aerr := st.InsertAlert(ctx, nil, "warn", msg); aerr != nil {
				log.Printf("xasset-monitor: still warn 落库失败: %v", aerr)
			}
		}
	}
	return firstErr
}

func runPair(ctx context.Context, st *pgstore.Store, p monitorPair, mtVenue string, exBid, exAsk float64) error {
	sym, pair := p.sym, p.pair
	var bid, ask, fundingRate float64
	var periodsPerDay int
	var err error
	if p.cexVenue == "okx:perp" {
		// OKX 公开行情路由（api/v5/market/ticker + public/funding-rate，无鉴权不走 proxy）。
		bid, ask, fundingRate, periodsPerDay, err = fetchOkxGoldFn(ctx, st.Pool(), sym)
		if err != nil {
			return fmt.Errorf("fetch okx %s: %w", sym, err)
		}
	} else if p.cexVenue == "gate:perp" {
		// Gate 公开行情路由（api/v4/futures/usdt tickers+contracts，无鉴权不走 proxy）。
		bid, ask, fundingRate, periodsPerDay, err = fetchGateGoldFn(ctx, st.Pool(), sym)
		if err != nil {
			return fmt.Errorf("fetch gate %s: %w", sym, err)
		}
	} else {
		bid, ask, fundingRate, periodsPerDay, err = fetchBinanceGoldFn(ctx, st.Pool(), sym)
		if err != nil {
			return fmt.Errorf("fetch binance %s: %w", sym, err)
		}
	}

	// 算指标（口径分离 D1）。
	entrySpread := (bid - exAsk) / exAsk * 10000
	exitSpread := (ask - exBid) / exBid * 10000
	// 三 venue VIP0 maker 费率同为 0.02%×2=4bps（binance 单 C 档；gate fee 表
	// gate.com/announcements/article/101365 VIP0 maker 0.0200%）→ 常量化共享。
	const cexMakerCostBps = 4.0
	exnessSpread := (exAsk - exBid) / exAsk * 10000
	entryNet := entrySpread - cexMakerCostBps - exnessSpread // 开+平净价差（maker 0.04%×2+Exness 点差）
	fundingAnnualPct := fundingRate * float64(periodsPerDay) * 365 * 100
	tokenMid := (bid + ask) / 2
	xauMid := (exBid + exAsk) / 2
	trackingErr := math.Abs(tokenMid-xauMid) / xauMid * 10000
	equivAnnual := entryNet/100 + fundingAnnualPct

	// noise_bps=trailing 24h 摆幅半径 (max−min)/2（含当前行；样本 <12 → NULL）。
	// 样本按 (pair,cex_venue,mt_venue) 三元组隔离——跨 venue 簿统计=借用伪半径。
	noiseBps := computeNoiseBps(append(trailing24hEntries(ctx, st.Pool(), pair, p.cexVenue, mtVenue), entrySpread))
	// harvest_floor=min(q25, entry−MIN_CAPTURE)——trailing 全深序列（含当前行）。
	harvestFloor := computeHarvestFloor(append(trailingAllEntries(ctx, st.Pool(), pair, p.cexVenue, mtVenue), entrySpread))

	rec := &pgstore.XassetMonitor{
		Pair:             pair,
		BinanceBid:       bid,
		BinanceAsk:       ask,
		ExnessBid:        exBid,
		ExnessAsk:        exAsk,
		EntrySpreadBps:   entrySpread,
		ExitSpreadBps:    exitSpread,
		EntryNetBps:      entryNet,
		FundingRate8h:    fundingRate,
		FundingAnnualPct: fundingAnnualPct,
		TrackingErrorBps: trackingErr,
		EquivAnnualPct:   equivAnnual,
		NoiseBps:         noiseBps,
		HarvestFloorBps:  harvestFloor,
		Src:              "live",
		CexVenue:         p.cexVenue,
		MtVenue:          mtVenue,
	}
	if err := st.InsertXassetMonitor(ctx, rec); err != nil {
		return fmt.Errorf("insert monitor: %w", err)
	}
	log.Printf("xasset-monitor: pair=%s entry_spread=%.2fbps entry_net=%.2fbps funding_annual=%.2f%% tracking_err=%.2fbps equiv_annual=%.2f%%",
		rec.Pair, entrySpread, entryNet, fundingAnnualPct, trackingErr, equivAnnual)

	// 逐 tick 市场状态阈值推送摘除（业主令——常态读数非业主事件；
	// 簿指标照常落库供面板/毕业链消费，fatal 故障走 pushFatalFeishu 保留）。
	return nil
}

// fetchBinanceGoldFn/fetchFundingPeriodsPerDayFn/fetchOkxTickerFn 测试 seam
// （生产=真实现；测试 stub 隔离 cex-proxy/公网）。
var (
	fetchBinanceGoldFn = fetchBinanceGold
	fetchOkxGoldFn     = fetchOkxGold
	fetchGateGoldFn    = fetchGateGold
)
