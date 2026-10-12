// wire_retention.go —— D-508 S3：facts/fin_cex_earn_apr 日轮 retention janitor。
// D-152 扩面：fin_cex_radar 14d 接线（PruneCexRadar 死代码激活）+ 五表 90d +
// cex_private_events 分档（证据帧 90d/心跳帧 7d）。
// 同款 [cex-depth] retention 惯例：collect.Named 24h 节奏，失败留痕不阻断，
// 产出零 facts（纯清理源）。
package main

import (
	"context"
	"log"

	"nox/internal/data/fact"
	"nox/internal/store/pgstore"
)

// retentionCollector facts kind 级 TTL + fin_cex_earn_apr 30d + ticks 90d 分区裁剪
// + D-152 五表 TTL + cex_private_events 分档（D-508 S3/S4、D-152）。
type retentionCollector struct{ st *pgstore.Store }

func (c *retentionCollector) Kind() string { return "retention" }

func (c *retentionCollector) Poll(ctx context.Context) ([]fact.Fact, error) {
	if n, err := c.st.PruneFacts(ctx); err != nil {
		log.Printf("[retention] facts prune: %v", err)
	} else if n > 0 {
		log.Printf("[retention] facts prune: purged %d rows", n)
	}
	if err := c.st.PruneCexEarnAPR(ctx, 30); err != nil {
		log.Printf("[retention] cex_earn_apr prune: %v", err)
	}
	if err := c.st.PruneCexDCIQuote(ctx, 30); err != nil {
		log.Printf("[retention] cex_dci_quote prune: %v", err)
	}
	if n, err := c.st.PruneTicks(ctx); err != nil {
		log.Printf("[retention] ticks prune: %v", err)
	} else if n > 0 {
		log.Printf("[retention] ticks prune: dropped %d partitions (>90d)", n)
	}
	// D-152①：fin_cex_radar 14d（PruneCexRadar 接线——读侧只消费最新扫描）。
	if err := c.st.PruneCexRadar(ctx, 14); err != nil {
		log.Printf("[retention] cex_radar prune: %v", err)
	}
	// D-152②：五表 90d（组终值卫兵——每分组至少留最新一行）。
	if pruned, err := c.st.PruneRetentionTables(ctx); err != nil {
		log.Printf("[retention] tables prune: %v", err)
	} else {
		for tbl, n := range pruned {
			if n > 0 {
				log.Printf("[retention] %s prune: purged %d rows", tbl, n)
			}
		}
	}
	// D-152③：cex_private_events 分档（证据帧 90d/心跳帧 7d）。
	if n, err := c.st.PruneCexPrivateEvents(ctx); err != nil {
		log.Printf("[retention] cex_private_events prune: %v", err)
	} else if n > 0 {
		log.Printf("[retention] cex_private_events prune: purged %d rows", n)
	}
	return nil, nil
}
