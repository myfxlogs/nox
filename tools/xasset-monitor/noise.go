// D-335 S1：noise_bps 噪声半径计算（自 main.go 拆出保行预算）。
package main

import (
	"context"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"
)

// trailing24hEntries 同 pair 最近 24h 已入库 entry_spread_bps 序列（S1 噪声半径样本）。
func trailing24hEntries(ctx context.Context, pool *pgxpool.Pool, pair string) []float64 {
	rows, err := pool.Query(ctx, `
		SELECT entry_spread_bps FROM fin_xasset_monitor
		WHERE pair=$1 AND ts > now()-interval '24 hours'`, pair)
	if err != nil {
		log.Printf("xasset-monitor: noise sample query pair=%s: %v (本次 NULL)", pair, err)
		return nil
	}
	defer rows.Close()
	var out []float64
	for rows.Next() {
		var v float64
		if err := rows.Scan(&v); err == nil {
			out = append(out, v)
		}
	}
	return out
}

// computeNoiseBps D-335 T4：摆幅半径=(max−min)/2；样本 <12 → NULL。
func computeNoiseBps(vals []float64) *float64 {
	if len(vals) < 12 {
		return nil
	}
	min, max := vals[0], vals[0]
	for _, v := range vals[1:] {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	half := (max - min) / 2
	return &half
}
