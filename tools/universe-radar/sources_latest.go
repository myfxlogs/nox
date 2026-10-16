// sources_latest.go —— 存量超标拆分（棘轮消化）：latestRadarMarkIndex/
// latestFacts/latestTicksMid 三件 DB 读径从 sources_binance.go 纯搬移，零行为变。
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"
)

func latestRadarMarkIndex(ctx context.Context, pool *pgxpool.Pool, venue string, symbols []string, maxAge string) (marks, idxs map[string]float64) {
	marks, idxs = map[string]float64{}, map[string]float64{}
	if pool == nil || len(symbols) == 0 {
		return marks, idxs
	}
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT ON (symbol) symbol, mark, idx FROM fin_cex_radar
		WHERE venue=$1 AND symbol=ANY($2) AND ts > now()-$3::interval
		ORDER BY symbol, ts DESC`, venue, symbols, maxAge)
	if err != nil {
		log.Printf("[universe-radar] radar mark/index 回填读败（留零如实缺）: %v", err)
		return marks, idxs
	}
	defer rows.Close()
	for rows.Next() {
		var sym string
		var m, ix float64
		if err := rows.Scan(&sym, &m, &ix); err != nil {
			return marks, idxs
		}
		marks[sym], idxs[sym] = m, ix
	}
	return marks, idxs
}

// latestFacts 批查 venue kind 最新值（src 非空则锚定；funding_interval 落点 src 异构不过滤）。
// 泛化 venue 参数供 okx/gate 读库化共用。
func latestFacts(ctx context.Context, pool *pgxpool.Pool, venue, kind string, srcs []string, maxAge string) map[string]float64 {
	q := `SELECT DISTINCT ON (symbol) symbol, value FROM facts
		WHERE kind=$1 AND venue=$2 AND ts > now()-$3::interval`
	args := []any{kind, venue, maxAge}
	if len(srcs) > 0 {
		q += ` AND src=ANY($4)`
		args = append(args, srcs)
	}
	q += ` ORDER BY symbol, ts DESC`
	out := map[string]float64{}
	if pool == nil {
		return out // D-205：无库=兜底面无（REST 主径下调用方可零库运行）
	}
	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		log.Printf("[universe-radar] %s facts %s: %v", venue, kind, err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var sym string
		var v float64
		if rows.Scan(&sym, &v) == nil {
			out[sym] = v
		}
	}
	return out
}

// latestTicksMid 批查 venue perp 最新 tick 中价（≈mark 近似）。
func latestTicksMid(ctx context.Context, pool *pgxpool.Pool, venue string) map[string]float64 {
	out := map[string]float64{}
	if pool == nil {
		return out
	}
	rows, err := pool.Query(ctx, `SELECT DISTINCT ON (symbol) symbol, (bid+ask)/2 FROM ticks
		WHERE venue=$1 AND venue_type='perp' AND ts > now()-interval '120 seconds'
		ORDER BY symbol, ts DESC`, venue)
	if err != nil {
		log.Printf("[universe-radar] %s ticks: %v", venue, err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var sym string
		var v float64
		if rows.Scan(&sym, &v) == nil {
			out[sym] = v
		}
	}
	return out
}

// DeliverySpreadBps P0 滑点前置：GET /fapi/v1/ticker/bookTicker?symbol=——
// 逐交割符号读盘口价差（4 符号低频 1h 轮，venue 公共端点无凭据）。
// bid/ask 破价（≤0/倒挂）→ err fail-closed，调用方标「滑点未知」不编。
func (b binance) DeliverySpreadBps(ctx context.Context, sym string) (float64, error) {
	var r struct {
		Bid flexFloat `json:"bidPrice"`
		Ask flexFloat `json:"askPrice"`
	}
	if err := getJSON(ctx, b.hc, b.base+"/fapi/v1/ticker/bookTicker?symbol="+sym, &r); err != nil {
		return 0, err
	}
	if r.Bid <= 0 || r.Ask < r.Bid {
		return 0, fmt.Errorf("bookTicker %s 价破 bid=%v ask=%v", sym, r.Bid, r.Ask)
	}
	mid := (float64(r.Bid) + float64(r.Ask)) / 2
	return (float64(r.Ask) - float64(r.Bid)) / mid * 1e4, nil
}
