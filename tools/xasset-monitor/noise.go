// D-335 S1：noise_bps 噪声半径计算（自 main.go 拆出保行预算）。
package main

import (
	"context"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"
)

// trailing24hEntries 同 (pair,cexVenue,mtVenue) 最近 24h 已入库 entry_spread_bps 序列
// （S1 噪声半径样本；D-423 三元组隔离——跨 venue 簿不混算）。
func trailing24hEntries(ctx context.Context, pool *pgxpool.Pool, pair, cexVenue, mtVenue string) []float64 {
	rows, err := pool.Query(ctx, `
		SELECT entry_spread_bps FROM fin_xasset_monitor
		WHERE pair=$1 AND cex_venue=$2 AND mt_venue=$3 AND ts > now()-interval '24 hours'`,
		pair, cexVenue, mtVenue)
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

// computeHarvestFloor D-341 T1 + D-014 ②：收割线=q25（MinCaptureBps 下限已删——
// 收割决策迁移 econ.NetIfClosedNow，floor 列停止消费仅留兼容写入；样本 <48 → NULL）。
// vals=同 pair trailing 全深 entry_spread 序列（含当前行）；q25=25 分位（线性插值）。
const harvestMinSamples = 48

func computeHarvestFloor(vals []float64) *float64 {
	if len(vals) < harvestMinSamples {
		return nil
	}
	sorted := append([]float64(nil), vals...)
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j] < sorted[i] {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	pos := 0.25 * float64(len(sorted)-1)
	lo := int(pos)
	hi := lo + 1
	if hi >= len(sorted) {
		hi = lo
	}
	q25 := sorted[lo] + (pos-float64(lo))*(sorted[hi]-sorted[lo])
	return &q25
}

// trailingAllEntries D-341 S1：同 (pair,cexVenue,mtVenue) 全深 entry_spread 序列
// （收割线窗口=可用史全深；D-423 三元组隔离）。
func trailingAllEntries(ctx context.Context, pool *pgxpool.Pool, pair, cexVenue, mtVenue string) []float64 {
	rows, err := pool.Query(ctx,
		`SELECT entry_spread_bps FROM fin_xasset_monitor
		 WHERE pair=$1 AND cex_venue=$2 AND mt_venue=$3 AND src='live'`,
		pair, cexVenue, mtVenue)
	if err != nil {
		log.Printf("xasset-monitor: harvest sample query pair=%s: %v (本次 NULL)", pair, err)
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
