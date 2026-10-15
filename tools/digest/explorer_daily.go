package main

// explorer_daily.go —— explorer daily 提案浮面（链路审计实证：scan_kind='daily'
// 行 write-only 零消费者；digest 段=最省投影面，提案产出从此可见可裁）。只读聚合件。

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// queuesSection 裁决+探矿两队列段聚合（main.go 行数约束拆出；读败各自跳过+log）。
func queuesSection(ctx context.Context, pool *pgxpool.Pool) string {
	var out string
	// 裁决队列浮面（D-521 续——proposed 积压推送非环境态）。
	aqN, aqTops, err := loadAdjudicationQueue(ctx, pool)
	if err != nil {
		log.Printf("digest: adjudication queue load: %v (段跳过)", err)
	} else {
		out += renderAdjudicationQueue(aqN, aqTops)
	}
	// 探矿日扫提案浮面（daily 行原 write-only 零消费——投影进 digest 使其可见可裁）。
	edN, edTops, err := loadExplorerDaily(ctx, pool)
	if err != nil {
		log.Printf("digest: explorer daily load: %v (段跳过)", err)
	} else {
		out += renderExplorerDaily(edN, edTops)
	}
	return out
}

// loadExplorerDaily 日扫提案计数+canonical top3（status='new'=未处置积压面；
// scan_kind='daily' 限定——probe 行有审批 RPC 消费径不在此段）。
func loadExplorerDaily(ctx context.Context, pool *pgxpool.Pool) (int, []string, error) {
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM explorer_proposals WHERE scan_kind='daily' AND status='new'`).Scan(&n); err != nil {
		return 0, nil, fmt.Errorf("explorer daily count: %w", err)
	}
	rows, err := pool.Query(ctx, `
		SELECT canonical, count(*) c
		FROM explorer_proposals WHERE scan_kind='daily' AND status='new'
		GROUP BY canonical ORDER BY c DESC, canonical LIMIT 3`)
	if err != nil {
		return 0, nil, fmt.Errorf("explorer daily top: %w", err)
	}
	defer rows.Close()
	var tops []string
	for rows.Next() {
		var k string
		var c int
		if err := rows.Scan(&k, &c); err != nil {
			return 0, nil, err
		}
		tops = append(tops, fmt.Sprintf("%s×%d", k, c))
	}
	return n, tops, rows.Err()
}

// renderExplorerDaily 「探矿日扫提案」行——N=0 照常印（同死角段防静默惯例）。
func renderExplorerDaily(n int, tops []string) string {
	if len(tops) == 0 {
		return fmt.Sprintf("探矿日扫提案：%d 条（daily new 积压）\n", n)
	}
	return fmt.Sprintf("探矿日扫提案：%d 条（daily new，top3：%s）\n", n, strings.Join(tops, "、"))
}
