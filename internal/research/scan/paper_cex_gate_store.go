// paper_cex_gate_store.go —— D-051②：CEX 纸面入口质量闸读面子接口（自
// paper_spot_carry.go 拆出，文件行数闸）。*pgstore.Store 原生实现。
package scan

import (
	"context"
	"time"

	"arb/internal/store/pgstore"
)

// PaperCexGateStore 质量闸读面：CexDepthAvgUSD=venue×canonical 窗内
// mid_depth_usd 均值+样本数（无样本 n=0，消费方 fail-closed 自判）；
// FundingSettleAvgs=since 起 funding_history 按 (venue,symbol) 均值。
type PaperCexGateStore interface {
	CexDepthAvgUSD(ctx context.Context, venue, canonical string, since time.Time) (avg float64, n int, err error)
	FundingSettleAvgs(ctx context.Context, since time.Time) (map[string]map[string]pgstore.SettleAvg, error)
}
