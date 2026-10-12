// backlog.go 待归纳积压闸（D-172）：未消解行 >30 或单行次数 >100 → InsertAlert warn。
// ✅ 消解行在 parsePendingHistory 已被剔除（只回 active），天然不计数；
// 告警走 alerts 表 → D-168 episode 制下游自然限推（一日一集）。
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"
)

// 积压闸阈（票面钦定）。
const (
	backlogRowLimit  = 30
	backlogCountMax  = 100
	backlogAlertKind = "warn"
)

// backlogStats 聚合行统计：行数 + 单行最大次数。
func backlogStats(agg []pendingEventAgg) (rows, maxCount int) {
	rows = len(agg)
	for _, e := range agg {
		if e.Count > maxCount {
			maxCount = e.Count
		}
	}
	return rows, maxCount
}

// backlogExceeded 超阈判。
func backlogExceeded(rows, maxCount int) bool {
	return rows > backlogRowLimit || maxCount > backlogCountMax
}

// backlogAlert 超阈时直插 alerts warn（pgxpool 直插——digest 是独立二进制不持 *pgstore.Store；
// episode 制在 wire_feishu 扇出端生效，本行仅落库，推送频控天然由下游保证）。
func backlogAlert(ctx context.Context, pool *pgxpool.Pool, rows, maxCount int) {
	if !backlogExceeded(rows, maxCount) {
		return
	}
	msg := fmt.Sprintf("经验库待归纳积压 %d 行（单行最大 %d 次，阈 行>%d/次>%d）", rows, maxCount, backlogRowLimit, backlogCountMax)
	if _, err := pool.Exec(ctx, `INSERT INTO alerts (level, message) VALUES ($1, $2)`, backlogAlertKind, msg); err != nil {
		log.Printf("digest: backlog alert insert: %v", err)
	}
}
