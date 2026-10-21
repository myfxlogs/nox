// coverage.go —— D-625 P1 数据覆盖账本自盯面：每拍读 ListXAssetCoverage，
// 饥饿（24h<12 行）/断供（最新行龄>采集周期×3）两型「数据不可判」warn 各一
// 发（dedup 24h），末行实施席转发令。纯读面零簿写。
// 在审键=当前派生清单（10-19 销账票）：退役组合/缩编 demand 的历史簿键是簿史
// 非断供——90d 保留窗内死键永久刷两型告警（首跑 28 对销账实证 27 对为死键），
// 活键才可能「饿」。
// 会话闸窗口化（10-19 销账票，替原「此刻闭市」点判——点判把周末整停周一晨
// 误报成断供）：断供=停更窗 (lastTs,now] 曾开市而簿未写才报（全落闭市内=静默
// 由闭市解释）；饥饿=12 行/24h 阈按窗内开市分钟折算（全开日=原阈不变、全闭窗
// 阈 0 恒豁免）。会话表缺席=无证据不误赦，原阈直判。
package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"nox/internal/data/keydomain"
	"nox/internal/data/marketstate"
	"nox/internal/platform/alerttext"
	"nox/internal/platform/rejects"
	"nox/internal/store/pgstore"
)

// rejects 注册：coverage_audit 面门集（D-652 拒因可观测——注册了不挂/挂了没注册由对拍锚防）。
func init() { rejects.Register("coverage_audit", "dead_key", "closed_window", "stale", "hungry") }

const (
	coverageHungryRows = 12               // 行数/全开日<12=饥饿（noise 阈同源）
	coverageStaleMax   = 15 * time.Minute // 最新行龄>采集周期(5min)×3=断供
	coverageDayMin     = 24 * 60          // 全开日分钟数（饥饿阈折算分母）
)

// coverageAudit 每拍覆盖自盯（runPairs 前搭车——零新定时器；pairs=同拍派生清单）。
func coverageAudit(ctx context.Context, st *pgstore.Store, pairs []monitorPair) {
	rows, err := st.ListXAssetCoverage(ctx)
	if err != nil {
		log.Printf("[coverage] 报表读败: %v", err)
		return
	}
	now := time.Now()
	sessTbl := marketstate.LoadSessionTable(ctx, st)
	live := make(map[string]bool, len(pairs))
	for _, p := range pairs {
		// 键序与行键一致：pair|cex|mt（行侧 fmt 同序——错序=全过滤死键假阳）。
		live[p.pair+"|"+p.cexVenue+"|"+p.mtVenueKey] = true
	}
	for _, r := range rows {
		key := fmt.Sprintf("%s|%s|%s", r.Pair, r.CexVenue, r.MtVenue)
		if !live[key] {
			rejects.Inc(rejects.Key{Face: "coverage_audit", Gate: "dead_key"})
			continue // 死键（组合退役/demand 缩编退场）——簿史不审计
		}
		// MT 腿 canonical=pair 尾段、venue=裸户号（与派生/会话表键同源）。
		mtCanon := r.Pair[strings.LastIndex(r.Pair, "-")+1:]
		mtBare := keydomain.BareOf(r.MtVenue)
		// 断供会话闸（窗口化）：停更窗内从未开市=静默由闭市解释，两型同豁免
		// （窗内零开市则 24h 行数低同理非饿）。
		if openSeen, known := sessTbl.OpenSince(mtBare, mtCanon, r.LastTs, now); known && !openSeen {
			rejects.Inc(rejects.Key{Face: "coverage_audit", Gate: "closed_window"})
			continue
		}
		if now.Sub(r.LastTs) > coverageStaleMax {
			rejects.Inc(rejects.Key{Face: "coverage_audit", Gate: "stale"})
			if aerr := st.InsertAlertDedup(ctx, "warn", fmt.Sprintf(
				"数据覆盖断供：%s 最新行龄 %s（>%s——簿停更=判定径不可判）%s",
				key, now.Sub(r.LastTs).Round(time.Minute), coverageStaleMax,
				alerttext.SeatDispatchLine("实施", fmt.Sprintf("覆盖账本断供排查 %s（读 STATE 本行）", r.Pair))), 24*time.Hour); aerr != nil {
				log.Printf("[coverage] stale alert: %v", aerr)
			}
		}
		// 饥饿阈开市折算：全开日=12 行原阈；窗内开市 openMin 分钟→阈=12×openMin/1440；
		// 表缺席（known=false）→原阈直判（无证据不误赦）。
		hungryThreshold := coverageHungryRows
		if openMin, known := sessTbl.OpenMinutes(mtBare, mtCanon, now.Add(-24*time.Hour), now); known {
			hungryThreshold = coverageHungryRows * openMin / coverageDayMin
		}
		if r.Rows24h < hungryThreshold {
			rejects.Inc(rejects.Key{Face: "coverage_audit", Gate: "hungry"})
			if aerr := st.InsertAlertDedup(ctx, "warn", fmt.Sprintf(
				"数据覆盖饥饿：%s 24h 仅 %d 行（<%d——数据不可判，回放窗禁准入）%s",
				key, r.Rows24h, hungryThreshold,
				alerttext.SeatDispatchLine("实施", fmt.Sprintf("覆盖账本排查 %s（读 STATE 本行——venue 供源/会话窗对拍）", r.Pair))), 24*time.Hour); aerr != nil {
				log.Printf("[coverage] hungry alert: %v", aerr)
			}
		}
	}
}
