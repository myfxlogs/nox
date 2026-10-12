// cex_depth.go —— D-420 S2：fin_cex_depth 顶档深度存取（容量与滑点研究数据前提；
// v1 顶档够定性，不做 >1 档；保留 90 天滚动由采集器侧 DeleteCexDepthBefore 兜底）。
package pgstore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// CexDepthRow fin_cex_depth 一行（顶档快照）。
type CexDepthRow struct {
	Venue       string
	Symbol      string
	Ts          time.Time
	Bid1USD     float64
	Ask1USD     float64
	Bid1Qty     float64
	Ask1Qty     float64
	MidDepthUSD float64
}

// InsertCexDepth 批量插入（采集器每轮一批，单事务原子）。
func (s *Store) InsertCexDepth(ctx context.Context, rows []CexDepthRow) error {
	batch := &pgx.Batch{}
	for _, r := range rows {
		batch.Queue(`INSERT INTO fin_cex_depth (venue, symbol, ts, bid1_usd, ask1_usd, bid1_qty, ask1_qty, mid_depth_usd)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, r.Venue, r.Symbol, r.Ts, r.Bid1USD, r.Ask1USD, r.Bid1Qty, r.Ask1Qty, r.MidDepthUSD)
	}
	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()
	for range rows {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("insert cex depth: %w", err)
		}
	}
	return nil
}

// depthBase canonical→深度采集基名（XAUUSDT/XAU-USDT-SWAP/XAU_USDT→XAU；
// 与 cmd/core carryDepthBase 同规则——剥 -USDT-SWAP 再剥 USDT，末清悬空分隔符）。
func depthBase(canonical string) string {
	b := strings.TrimSuffix(canonical, "-USDT-SWAP")
	b = strings.TrimSuffix(b, "USDT")
	return strings.TrimRight(b, "_-")
}

// CexDepthAvgUSD D-051②a：venue×canonical 窗内 mid_depth_usd 均值+样本数
// （纸面入口深度闸）。symbol 匹配三所原生形态全集（baseUSDT/base_USDT/
// base-USDT-SWAP）——每所只写一种形态，IN 集匹配不跨所串行；无样本 n=0
// （消费方 fail-closed 自判）。
func (s *Store) CexDepthAvgUSD(ctx context.Context, venue, canonical string, since time.Time) (avg float64, n int, err error) {
	base := depthBase(canonical)
	err = s.pool.QueryRow(ctx,
		`SELECT COALESCE(avg(mid_depth_usd),0), count(*) FROM fin_cex_depth
		 WHERE venue=$1 AND ts>=$2 AND symbol IN ($3,$4,$5)`,
		venue, since, base+"USDT", base+"_USDT", base+"-USDT-SWAP").Scan(&avg, &n)
	if err != nil {
		return 0, 0, fmt.Errorf("cex depth avg %s %s: %w", venue, canonical, err)
	}
	return avg, n, nil
}

// DeleteCexDepthBefore 保留期滚动清理（radar 90 天自净同族）。
func (s *Store) DeleteCexDepthBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	ct, err := s.pool.Exec(ctx, `DELETE FROM fin_cex_depth WHERE ts < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("delete cex depth: %w", err)
	}
	return ct.RowsAffected(), nil
}
