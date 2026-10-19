// coverage.go —— D-625 P1 数据覆盖账本自盯面：每拍读 ListXAssetCoverage，
// 饥饿（24h<12 行）/断供（最新行龄>采集周期×3）两型「数据不可判」warn 各一
// 发（dedup 24h），末行实施席转发令。纯读面零簿写。
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"nox/internal/platform/alerttext"
	"nox/internal/store/pgstore"
)

const (
	coverageHungryRows = 12               // 行数/24h<12=饥饿（noise 阈同源）
	coverageStaleMax   = 15 * time.Minute // 最新行龄>采集周期(5min)×3=断供
)

// coverageAudit 每拍覆盖自盯（runPairs 后搭车——零新定时器）。
func coverageAudit(ctx context.Context, st *pgstore.Store) {
	rows, err := st.ListXAssetCoverage(ctx)
	if err != nil {
		log.Printf("[coverage] 报表读败: %v", err)
		return
	}
	now := time.Now()
	for _, r := range rows {
		key := fmt.Sprintf("%s|%s|%s", r.Pair, r.CexVenue, r.MtVenue)
		if r.Rows24h < coverageHungryRows {
			if aerr := st.InsertAlertDedup(ctx, "warn", fmt.Sprintf(
				"数据覆盖饥饿：%s 24h 仅 %d 行（<%d——数据不可判，回放窗禁准入）%s",
				key, r.Rows24h, coverageHungryRows,
				alerttext.SeatDispatchLine("实施", fmt.Sprintf("覆盖账本排查 %s（读 STATE 本行——venue 供源/会话窗对拍）", r.Pair))), 24*time.Hour); aerr != nil {
				log.Printf("[coverage] hungry alert: %v", aerr)
			}
		}
		if now.Sub(r.LastTs) > coverageStaleMax {
			if aerr := st.InsertAlertDedup(ctx, "warn", fmt.Sprintf(
				"数据覆盖断供：%s 最新行龄 %s（>%s——簿停更=判定径不可判）%s",
				key, now.Sub(r.LastTs).Round(time.Minute), coverageStaleMax,
				alerttext.SeatDispatchLine("实施", fmt.Sprintf("覆盖账本断供排查 %s（读 STATE 本行）", r.Pair))), 24*time.Hour); aerr != nil {
				log.Printf("[coverage] stale alert: %v", aerr)
			}
		}
	}
}
