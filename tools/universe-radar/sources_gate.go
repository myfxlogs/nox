package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ---------- Gate（D-591 R3：纯读库，REST contracts/tickers 退役） ----------
// gate futures.tickers WS 已全市场落 ticks/facts（perp 符号面实测在库）——
// 宇宙枚举=ticks 24h 活跃符号（24h 无报价=视同退场符号），funding/interval/mark 读库，
// 未覆盖字段（LaunchTS/CtVal/Index/Vol24h 无库源）诚实零值不猜。
type gate struct {
	hc   *http.Client // D-205②：funding 公共 REST（contracts 单调用）
	base string
	pool *pgxpool.Pool
}

func (g gate) Venue() string { return "gate" }

func (g gate) Scan(ctx context.Context) ([]Row, error) {
	// 宇宙：近 24h 有 tick 的 gate perp 符号（frame 全市场推送；24h 静默=退场视同除名）。
	syms, err := g.symbols(ctx)
	if err != nil {
		return nil, err
	}
	// D-205②：funding 换 venue 公共 REST——/api/v4/futures/usdt/contracts 单调用全市场
	// （funding_rate+funding_interval 原生字段）；读败→facts 兜底参考。
	fundREST, rerr := g.fundingContracts(ctx)
	if rerr != nil {
		log.Printf("[universe-radar] gate contracts 读败（facts funding 兜底）: %v", rerr)
	}
	fund := latestFacts(ctx, g.pool, "gate", "funding", []string{"gate_ws_futures"}, "15 minutes")
	ivl := latestFacts(ctx, g.pool, "gate", "funding_interval", nil, "25 hours") // 日轮写源（D-591 R1 同定标）
	mid := latestTicksMid(ctx, g.pool, "gate")
	funded := 0
	rows := make([]Row, 0, len(syms))
	for _, sym := range syms { // sym=canonical BTCUSDT 形（canonicalFromGate 键形）
		r := Row{
			Symbol: sym, Base: strings.TrimSuffix(sym, "USDT"),
			Status: "TRADING", // 24h 在报价=在交易中（库内无原生 status 列，不猜）
			Mark:   mid[sym],
			CtVal:  f64ptr(0), // quanto_multiplier 无库源 → nil（诚实缺）
		}
		if fr, ok := fundREST[sym]; ok && fr.ivlH > 0 {
			r.FundingRate = fr.rate // REST 原生单期费率
			r.IntervalH = fr.ivlH
			funded++
		} else if ap, ok := fund[sym]; ok {
			if sec, ok2 := ivl[sym]; ok2 && sec > 0 {
				// 年化% → 单期小数（binance 源同式）；interval 秒 → 小时
				r.FundingRate = ap * sec / (365 * 100 * 86400)
				r.IntervalH = int(math.Round(sec / 3600))
				funded++
			}
		}
		rows = append(rows, r)
	}
	fundNote("gate", len(rows), funded)
	return rows, nil
}

// gateFunding D-205②：contracts 行→{rate 单期小数, ivlH 小时}。
type gateFunding struct {
	rate float64
	ivlH int
}

// fundingContracts GET /api/v4/futures/usdt/contracts——全市场合约面 funding
// 字段直取（venue 官方公共端点无凭据；canonical 键形 BTC_USDT→BTCUSDT）。
func (g gate) fundingContracts(ctx context.Context) (map[string]gateFunding, error) {
	if g.hc == nil || g.base == "" {
		return nil, fmt.Errorf("REST 面未接线（facts 兜底）") // 库源单跑/测试形态
	}
	var arr []struct {
		Name            string    `json:"name"`
		FundingRate     flexFloat `json:"funding_rate"`
		FundingInterval flexInt   `json:"funding_interval"` // 秒
	}
	if err := getJSON(ctx, g.hc, g.base+"/api/v4/futures/usdt/contracts", &arr); err != nil {
		return nil, err
	}
	out := make(map[string]gateFunding, len(arr))
	for _, c := range arr {
		if !strings.HasSuffix(c.Name, "_USDT") {
			continue
		}
		out[strings.ReplaceAll(c.Name, "_", "")] = gateFunding{
			rate: float64(c.FundingRate), ivlH: int(math.Round(float64(c.FundingInterval) / 3600))}
	}
	return out, nil
}

// symbols gate perp 宇宙枚举（ticks 24h 活跃符号）。
func (g gate) symbols(ctx context.Context) ([]string, error) {
	rows, err := g.pool.Query(ctx, `SELECT DISTINCT symbol FROM ticks
		WHERE venue='gate' AND venue_type='perp' AND ts > now()-interval '24 hours' ORDER BY symbol`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if rows.Scan(&s) == nil {
			out = append(out, s)
		}
	}
	return out, rows.Err()
}
