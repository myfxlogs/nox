// 拆分：Gate funding 结算历史采集器（自 exchange_history.go 移出保行预算）+
// per-symbol 结算周期数据驱动推导（gate_period.go 众数器）。
package exchange

import (
	"context"
	"fmt"
	"time"

	"arb/internal/data/fact"
)

// GateFundingHistory 采集 Gate funding 结算历史。
// 端点 GET /api/v4/futures/usdt/funding_rate?contract=&from=&to=&limit=1000
// 响应（实测 2026-08-31）：[{"r":"rate(string)","t":sec}]——t 是秒非毫秒（紧凑格式）。
// limit 1000（Gate API 上限，实测 limit=2000 返回 HTTP 400）；30 天 8h=90 条，1h 结算最坏 720 条，1000 够。
type GateFundingHistory struct {
	cfg     Config
	resolve HistoryStartResolver
	derived map[string]*gatePeriodDeriver // D-334ab：per-symbol 周期推导器（跨页/跨轮滚动）
}

func NewGateFundingHistory(cfg Config, r HistoryStartResolver) *GateFundingHistory {
	return &GateFundingHistory{cfg: cfg, resolve: r}
}

// deriverFor per-symbol 推导器懒初始化。
func (g *GateFundingHistory) deriverFor(pair string) *gatePeriodDeriver {
	if g.derived == nil {
		g.derived = map[string]*gatePeriodDeriver{}
	}
	d, ok := g.derived[pair]
	if !ok {
		d = newGatePeriodDeriver()
		g.derived[pair] = d
	}
	return d
}

func (g *GateFundingHistory) Kind() string { return fact.KindFundingHistory }

func (g *GateFundingHistory) Poll(ctx context.Context) ([]fact.Fact, error) {
	var out []fact.Fact
	for _, sym := range g.syms() {
		pair := sym + "USDT"
		start, err := startTS(ctx, g.resolve, VenueGate, pair)
		if err != nil {
			// 逐符号 fail-open（同 Binance）。
			fmt.Printf("[exchange:gate] funding_history %s: %v (skip)\n", pair, err)
			continue
		}
		contract := sym + "_USDT"
		url := fmt.Sprintf("%s/api/v4/futures/usdt/funding_rate?contract=%s&from=%d&to=%d&limit=1000",
			g.cfg.GateBaseURL, contract, start.Unix(), time.Now().Unix())
		arr, err := getJSONArray(ctx, g.cfg.Client, url)
		if err != nil {
			fmt.Printf("[exchange:gate] funding_history %s: %v (skip)\n", pair, err)
			continue
		}
		// 位点③：页内先全部喂推导器（众数含本页行），再按推导周期年化——
		// 4h 品种历史年化不再恒 ×3 低估（binance=缓存/okx=探测已在位，此处为最后残留）。
		deriver := g.deriverFor(pair)
		tSecs := make([]float64, 0, len(arr))
		for _, item := range arr {
			if m, ok := item.(map[string]any); ok {
				tSec, _ := m["t"].(float64) // Gate t = Unix 秒
				tSecs = append(tSecs, tSec)
			}
		}
		for _, tSec := range tSecs {
			deriver.observe(tSec)
		}
		periods := deriver.periodsPerDay()
		for _, item := range arr {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			rateStr, _ := m["r"].(string)
			tSec, _ := m["t"].(float64) // Gate t = Unix 秒
			var f float64
			fmt.Sscanf(rateStr, "%f", &f)
			out = append(out, fact.Fact{
				Kind: fact.KindFundingHistory, Venue: VenueGate, Symbol: pair,
				Value: annualizeFunding(f, periods),
				Unit:  fact.UnitPctAnnualized, Ts: time.Unix(int64(tSec), 0), Src: "gate_v4_history",
			})
		}
	}
	return out, nil
}

// syms D-369 S2：gate funding history 符号集。
func (g *GateFundingHistory) syms() []string { return g.cfg.effectiveSymbols("gate") }
