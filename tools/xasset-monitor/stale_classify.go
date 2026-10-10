// stale_classify.go —— D-160：薄品种 stale 分级（簿面静止 vs 真断流）。
// 病灶链：readGoldTicks 120s 阈按全品种 6h 间隔定标，但 okx tickers 变更驱动——
// 薄品种簿面不动时单品种超阈是常态（D-155 实证 XAUUSDT 中位 70s/6h 内 11 次 >120s
// 断档），却走 firstErr→reportFatal→critical+飞书直推 = 可预期稀疏被升 fatal。
// 分级：stale 命中后查「venue×数据源活性锚」（同 venue+venue_type 任一 ticks 行
// <120s 鲜 / funding 同 kind 任一 facts 鲜行）——锚成立=簿面静止降 warn 落行
// 不推飞书；锚不成立=真断流维持 critical；超 stillHardCap 即使锚成立也升级
// （单品种被 venue 侧静默摘订阅的残余面，对齐 D-155 展示上限 30min）。
// 禁区：stale 行永不入 fin_xasset_monitor（pair 仍 skip）；120s 阈本身不动。
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// stillHardCap 簿面静止硬帽（对齐 D-155 展示上限）——超帽=订阅疑似被摘，升 dead。
const stillHardCap = 30 * time.Minute

// staleDataErr 数据源 stale 命中（kind=ticks|facts_kind；venue=裸名；age=行龄；
// age=0=无行——比超帽更劣，分级按 dead 处理）。
type staleDataErr struct {
	kind   string // "ticks" | "funding" | "funding_interval"
	venue  string
	symbol string
	age    time.Duration
}

// Error 保留原 fmt 文案形态（消费端断言/日志归一化键不漂移）。
func (e *staleDataErr) Error() string {
	switch e.kind {
	case "ticks":
		if e.age == 0 {
			return fmt.Sprintf("ticks %s: no rows in result set", e.symbol)
		}
		return fmt.Sprintf("ticks %s stale age=%v > 120s", e.symbol, e.age.Round(time.Second))
	case "funding":
		if e.age == 0 {
			return fmt.Sprintf("facts funding %s: no rows in result set", e.symbol)
		}
		return fmt.Sprintf("facts funding %s stale age=%v > 15m", e.symbol, e.age.Round(time.Second))
	default: // funding_interval
		if e.age == 0 {
			return fmt.Sprintf("facts funding_interval %s: no rows in result set", e.symbol)
		}
		return fmt.Sprintf("facts funding_interval %s stale age=%v > 25h", e.symbol, e.age.Round(time.Second))
	}
}

// venueAnchor 活性锚查询：同 venue 同数据源任一鲜行即成立（kind→表/时窗映射）。
// 查询失败=保守 dead（宁误警不漏断流——锚失效无法举证活）。
func venueAnchor(ctx context.Context, pool *pgxpool.Pool, e *staleDataErr) bool {
	var n int
	var err error
	switch e.kind {
	case "ticks":
		err = pool.QueryRow(ctx, `SELECT count(*) FROM ticks
			WHERE venue=$1 AND venue_type='perp' AND ts > now() - interval '120 seconds'`,
			e.venue).Scan(&n)
	default: // funding / funding_interval 同 kind 鲜行（funding=15m 守门窗、interval=25h）
		win := "15 minutes"
		if e.kind == "funding_interval" {
			win = "25 hours"
		}
		err = pool.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM facts
			WHERE kind=$2 AND venue=$1 AND ts > now() - interval '%s'`, win),
			e.venue, e.kind).Scan(&n)
	}
	return err == nil && n > 0
}

// venueAnchorFn 测试 seam（生产=venueAnchor）。
var venueAnchorFn = venueAnchor

// classifyStale 分级：dead=真断流（critical+飞书）；still=簿面静止（warn 落行不推）。
// 判定序：无行→dead（超帽劣化）→超帽→dead（订阅疑似被摘）→锚鲜→still→锚死→dead。
func classifyStale(ctx context.Context, pool *pgxpool.Pool, e *staleDataErr) string {
	if e.age == 0 || e.age > stillHardCap {
		return "dead"
	}
	if venueAnchorFn(ctx, pool, e) {
		return "still"
	}
	return "dead"
}
