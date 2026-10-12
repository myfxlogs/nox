// D-334ab S3：binance income 实数轨采集装配（仿 wire_cexscan.go）——
// 结算对齐定时器：对 binance symbol 集（白名单∪OPEN 仓腿）取 FundingIntervalCache
// 周期真值算下一结算边界，+1min 触发一轮采集；无缓存品种回退 8h 边界+warn（D-079 禁静默）。
package main

import (
	"context"
	"log"
	"strings"
	"time"

	"nox/internal/api/dashboard"
	"nox/internal/data/collect/exchange"
	"nox/internal/platform/safego"
	"nox/internal/store/pgstore"
)

// startIncomeCollector 装配并启动 income 采集（collector+周期缓存+定时器一体）。
// 周期缓存独立实例（buildCexFundingCollectors 的实例为函数内私有，此处 boot 再拉一次
// fundingInfo——公共只读端点，成本可忽略；失败 best-effort 回退 8h 边界）。
// incomeKickSettleDelay D-469a S4：kick 收到后延迟 RunOnce——bills/ledger 入账有
// 数秒延迟，30s 后拉取覆盖大多数到账窗（定时边界路径仍在=双保险幂等去重）。
const incomeKickSettleDelay = 30 * time.Second

// waitOrKick D-469a S4：定时等待或事件 kick 先到先跑（kick 后 caller 再延迟
// incomeKickSettleDelay 等入账）。返回 (kicked, ctxDone)。
func waitOrKick(ctx context.Context, wait time.Duration, kickCh chan struct{}) (kicked, done bool) {
	select {
	case <-ctx.Done():
		return false, true
	case <-time.After(wait):
		return false, false
	case <-kickCh:
		return true, false
	}
}

func startIncomeCollector(ctx context.Context, st *pgstore.Store, cexCfg exchange.Config, whitelist []string, kickCh chan struct{}) {
	intervals := exchange.NewFundingIntervalCache()
	if err := intervals.Populate(ctx, cexCfg); err != nil {
		log.Printf("[wiring] income fundingIntervalCache populate (best-effort, fallback 8h boundary): %v", err)
	}
	col := exchange.NewBinanceIncomeCollector(cexCfg, st, func() []exchange.BinanceIncomeAccount {
		exchs, err := st.ListExchangeConfigs(ctx)
		if err != nil {
			log.Printf("[wiring] income ListExchangeConfigs: %v (round skipped)", err)
			return nil
		}
		var out []exchange.BinanceIncomeAccount
		for _, c := range exchs {
			if c.Venue != "binance" || c.VenueType != "perp" || c.APIKey == "" || c.APISecret == "" || c.Simulated {
				continue // 配置驱动：有凭据的 binance perp 行才采集（D-516：simulated=t 行跳过——demo 域已收编）
			}
			base := c.BaseURL
			if base == "" {
				base = cexCfg.BinanceBaseURL
			}
			// D-362 S5：Simulated 透传（环境一致性匹配——D-516 起 sim 行上游已 skip，字段为纯标记位）。
			out = append(out, exchange.BinanceIncomeAccount{ID: c.ID, APIKey: c.APIKey, APISecret: c.APISecret, BaseURL: base, Simulated: c.Simulated})
		}
		return out
	})
	safego.GoLoop("main:income", func() {
		for {
			// D-466 S3：binance 自家 funding_next_settle fact 精确时刻优先（venue 原生
			// 真值防漂移）；无 fact 品种回退 interval 边界（venue 原生合法）。
			settles := map[string]time.Time{}
			if fs, err := st.LatestFacts(ctx, "funding_next_settle", "binance", ""); err == nil {
				for _, f := range fs {
					if f.Value > 0 {
						settles[f.Symbol] = time.Unix(int64(f.Value), 0).UTC()
					}
				}
			}
			wait, missing := nextIncomeBoundary(time.Now().UTC(), intervals, settles, whitelist, func() ([]string, error) {
				return st.OpenLegSymbols(ctx, "binance:perp") // D-362 S5：venue 全形=腿口径
			})
			if missing > 0 {
				log.Printf("[wiring] income boundary: %d symbols no interval cache (fallback 8h boundary)", missing)
			}
			// D-469a S4：FUNDING_FEE 事件 kick 支路——先到先跑（结算边界定时仍兜底）；
			// kick 后延迟等 bills 入账，RunOnce 水位去重天然幂等。
			kicked, done := waitOrKick(ctx, wait, kickCh)
			if done {
				return
			}
			if kicked {
				select {
				case <-ctx.Done():
					return
				case <-time.After(incomeKickSettleDelay):
				}
			}
			if n, err := col.RunOnce(ctx); err != nil {
				log.Printf("[wiring] income RunOnce: %v", err)
			} else if n > 0 {
				log.Printf("[wiring] income round: %d events inserted (kicked=%v)", n, kicked)
			}
		}
	})
	log.Printf("[wiring] income collector started (settlement-aligned, whitelist=%v)", whitelist)
}

// startOKXIncomeCollector D-385 S3：okx bills→income_events 装配。
// accounts 配置驱动同 binance 先例（venue=okx && venue_type=perp && apiKey/apiSecret/passphrase
// 非空；simulated=t 的 demo 行也采——demo sim 腿归因同轨）。定时器=固定 15min 轮询：
// 账本归因容忍分钟级延迟，OKX per-symbol 结算周期需额外 public 拉取，为对齐多花一套缓存不值得。
func startOKXIncomeCollector(ctx context.Context, st *pgstore.Store, cexCfg exchange.Config, kickCh chan struct{}) {
	col := exchange.NewOKXIncomeCollector(cexCfg, st, func() []exchange.OKXIncomeAccount {
		exchs, err := st.ListExchangeConfigs(ctx)
		if err != nil {
			log.Printf("[wiring] okx income ListExchangeConfigs: %v (round skipped)", err)
			return nil
		}
		var out []exchange.OKXIncomeAccount
		for _, c := range exchs {
			if c.Venue != "okx" || c.VenueType != "perp" || c.APIKey == "" || c.APISecret == "" || c.Passphrase == "" || c.Simulated {
				continue // D-516：simulated=t 行跳过（demo 域已收编——x-simulated 头支路删除，硬拨实盘域必鉴权失败）
			}
			base := c.BaseURL
			if base == "" {
				base = cexCfg.OKXBaseURL
			}
			out = append(out, exchange.OKXIncomeAccount{ID: c.ID, APIKey: c.APIKey, APISecret: c.APISecret, Passphrase: c.Passphrase, BaseURL: base, Simulated: c.Simulated})
		}
		return out
	})
	safego.GoLoop("main:okx_income", func() {
		for {
			if n, err := col.RunOnce(ctx); err != nil {
				log.Printf("[wiring] okx income RunOnce: %v", err)
			} else if n > 0 {
				log.Printf("[wiring] okx income round: %d events inserted", n)
			}
			// D-469a S4：balance_and_position funding_fee kick 支路（修永久正确性缺口：
			// 边界后 <15min 平仓→position_id 永久 NULL 不可愈）；15min 兜底保留。
			kicked, done := waitOrKick(ctx, 15*time.Minute, kickCh)
			if done {
				return
			}
			if kicked {
				select {
				case <-ctx.Done():
					return
				case <-time.After(incomeKickSettleDelay):
				}
			}
		}
	})
	log.Printf("[wiring] okx income collector started (15min poll)")
}

// nextIncomeBoundary 下一结算边界+1min 的等待时长；missing=无周期缓存品种数（warn 由调用方打）。
// 边界优先取 binance 自家 funding_next_settle fact 精确时刻（venue 原生，D-466）；
// 无 fact → UTC 零点起算 k×(24/periods)h 的下一个；无任何真值品种 → 8h 边界回退。
func nextIncomeBoundary(now time.Time, intervals *exchange.FundingIntervalCache, settles map[string]time.Time, whitelist []string, openSymbols func() ([]string, error)) (time.Duration, int) {
	syms := append([]string{}, whitelist...)
	if open, err := openSymbols(); err != nil {
		log.Printf("[wiring] income open leg symbols: %v (whitelist only)", err)
	} else {
		syms = append(syms, open...)
	}
	if len(syms) == 0 {
		return nextUTCBoundary(now, 8).Add(time.Minute).Sub(now), 0
	}
	var best time.Time
	missing := 0
	for _, sym := range syms {
		// D-466 S3：venue 原生精确时刻优先（settles key=venue pair XAUUSDT；canonical 补试 +USDT）。
		next, ok := settles[sym]
		if !ok && !strings.HasSuffix(sym, "USDT") {
			next, ok = settles[sym+"USDT"]
		}
		if ok && next.After(now) {
			if best.IsZero() || next.Before(best) {
				best = next
			}
			continue
		}
		// D-334c S0：白名单/OpenLegSymbols 供 canonical（XAU），缓存键=venue pair（XAUUSDT）
		// ——miss 时补试 +USDT，两 miss 才计 missing（生产实证不补试=30 symbols 全量回退）。
		periods, ok2 := intervals.Lookup(sym)
		if !ok2 && !strings.HasSuffix(sym, "USDT") {
			periods, ok2 = intervals.Lookup(sym + "USDT")
		}
		if !ok2 {
			missing++
			periods = 3 // 8h 回退
		}
		next = nextUTCBoundary(now, 24/periods)
		if best.IsZero() || next.Before(best) {
			best = next
		}
	}
	return best.Add(time.Minute).Sub(now), missing
}

// nextUTCBoundary now（UTC）之后的下一个整 k×hours 边界（恰在边界上→取下一个）。
// D-337R1 S2 下沉：逻辑归 dashboard.NextUTCBoundaryHours（CEX funding 结算边界共用，消双份）。
func nextUTCBoundary(now time.Time, hours int) time.Time {
	return dashboard.NextUTCBoundaryHours(now, hours)
}
