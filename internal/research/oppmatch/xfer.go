// xfer.go —— 跨 venue 机会差调拨提案（业主令 10-14「钱跟利走」配套+D-612 扩面）。
// 同资产跨所 earn APR 差超阈+源池有可动量→fin_condition_gap 投资人侧请求行。
// 系统只提案永不执行划转（充提币=业主域）；比价源缺一方/陈旧 fail-closed 不比；
// 阈值 app_config xfer_apr_diff_pct（pp 点，缺省 5）可改不硬编。
// D-612：源域 idle 门→低效仓门——闲池 ∪ venue_type='earn' 已部署仓皆入源
// （低效仓零容忍；earn 仓带持有窗守卫防费耗抖动）。
// D-613 补正③：提案行带 CEX 集中度透出（dst 后占比 vs 35% 硬线读数）供回执判断。
package oppmatch

import (
	"context"
	"fmt"
	"log"
	"time"

	"nox/internal/store/pgstore"
)

// xferTemplateID 调拨提案模板标识（fin_condition_gap.template_id 非 FK——哨兵值即可）。
const xferTemplateID = "cross_venue_xfer"

// xferCCYs 先行域=USDT（票面口径；扩列=改此集+锚）。
var xferCCYs = map[string]bool{"USDT": true}

// xferScan 并入扫描拍（零新定时器）。同对同向 gap 行 UNIQUE 幂等零重推；
// 业主收口（fulfilled）后差仍存→RequestConditionGap 复推（D-196② 既有语义）。
func xferScan(ctx context.Context, st *pgstore.Store) {
	th := 5.0
	if v, ok, err := st.AppConfigFloat(ctx, "xfer_apr_diff_pct"); err == nil && ok && v > 0 {
		th = v
	}
	// D-612③ 弹药水位+持有窗（轮动守卫，app_config 落表）。
	ammoFloor := 500.0
	if v, ok, err := st.AppConfigFloat(ctx, "rotation_ammo_floor_usd"); err == nil && ok && v >= 0 {
		ammoFloor = v
	}
	minHoldH := 168.0
	if v, ok, err := st.AppConfigFloat(ctx, "rotation_min_hold_hours"); err == nil && ok && v > 0 {
		minHoldH = v
	}
	aprs, err := st.LatestCexEarnAPRs(ctx)
	if err != nil {
		log.Printf("[xfer] earn apr 读败不比: %v", err)
		return
	}
	pools, err := st.ExplorerIdleFunds(ctx, time.Now())
	if err != nil {
		log.Printf("[xfer] idle 读败不比: %v", err)
		return
	}
	lm := fetchLiveEquityMap(ctx) // D-604 P2③：拍首单次实拉（禁逐池 Refresh 风暴）
	// venue→apr（每 ccy）；>24h 陈旧行剔——比价源不新鲜不比。
	// 促销档剔除：APR 列=基础档真值，tier_apr 促销件只存不入判据（D-514 窗口径）。
	best := map[string]map[string]float64{}
	for _, r := range aprs {
		if !xferCCYs[r.Ccy] || r.ProductType != "flexible" || time.Since(r.Ts) > 24*time.Hour {
			continue
		}
		if best[r.Ccy] == nil {
			best[r.Ccy] = map[string]float64{}
		}
		if r.APR > best[r.Ccy][r.Venue] {
			best[r.Ccy][r.Venue] = r.APR
		}
	}
	// venue→可动池权益（每 ccy；D-612 低效仓门：闲池 ∪ earn 已部署仓）。
	movableEq := map[string]map[string]float64{}
	earnPool := map[string]bool{} // venue→有 earn 已部署仓（持有窗守卫仅绑 earn 源）
	// 集中度透出读数（D-613③）：全池权益总+venue 权益（USD 口径跨币种）。
	totEq := 0.0
	venueEq := map[string]float64{}
	nonEarnEq := map[string]float64{}
	for _, p := range pools {
		if p.Ghost {
			continue
		}
		eq, ok := lm.poolEquity(p.Venue, p.VenueType, p.Currency, p.Equity, "[xfer]") // 无流池实拉（分币键）
		if !ok || eq <= 0 {
			continue // 拉败/映射缺=如实跳过（禁显示陈值）
		}
		totEq += eq
		venueEq[p.Venue] += eq
		if p.VenueType != "earn" {
			nonEarnEq[p.Venue] += eq
		}
		if !xferCCYs[p.Currency] {
			continue
		}
		if p.VenueType == "earn" {
			earnPool[p.Venue] = true
		}
		if p.Idle || p.VenueType == "earn" {
			if movableEq[p.Currency] == nil {
				movableEq[p.Currency] = map[string]float64{}
			}
			movableEq[p.Currency][p.Venue] += eq
		}
	}
	for ccy, vs := range best {
		// dst=APR 最高所；src=有 APR 行（比价源齐=fail-closed 前提）+可动池非零。
		dst, dstAPR := "", -1.0
		for v, a := range vs {
			if a > dstAPR {
				dst, dstAPR = v, a
			}
		}
		for src, srcAPR := range vs {
			// APR 列=小数（0.01=1%）；阈值 xfer_apr_diff_pct=pp 点——差先×100 再比。
			if src == dst || srcAPR >= dstAPR || (dstAPR-srcAPR)*100 < th {
				continue
			}
			amt := movableEq[ccy][src]
			if amt <= 0 {
				continue
			}
			// earn 仓源守卫：持有窗内不议（防费耗抖动——D-612③；闲池源不绑）。
			if earnPool[src] {
				if ts, has, aerr := st.LatestEarnActivityAt(ctx, src); aerr != nil {
					log.Printf("[xfer] %s earn 活动史读败跳过: %v", src, aerr)
					continue
				} else if has && time.Since(ts) < time.Duration(minHoldH)*time.Hour {
					continue
				}
			}
			subject := fmt.Sprintf("%s:%s>%s", ccy, src, dst)
			// 集中度透出（补正③）：划转后 dst 占比 vs 35% 硬线读数随文走。
			conc := ""
			if totEq > 0 {
				postDst := (venueEq[dst] + amt) / totEq * 100
				conc = fmt.Sprintf("；集中度：%s 划转后占总权益 %.1f%%（硬线 35%%）", dst, postDst)
			}
			ammo := ""
			if nonEarnEq[src] < ammoFloor {
				ammo = fmt.Sprintf("；⚠ %s 弹药水位 %.0f<floor %.0f", src, nonEarnEq[src], ammoFloor)
			}
			id, err := st.OpenConditionGap(ctx, &pgstore.ConditionGap{
				TemplateID: xferTemplateID, Kind: "transfer", Subject: subject,
				RequestText: fmt.Sprintf(
					"跨所收益差超阈：建议划转 %.2f %s %s→%s（earn APR %.2f%%→%.2f%%，差 %.1fpt%s%s）——划转属业主域，系统只提案不执行",
					amt, ccy, src, dst, srcAPR*100, dstAPR*100, (dstAPR-srcAPR)*100, conc, ammo),
			})
			if err != nil {
				log.Printf("[xfer] open gap %s: %v", subject, err)
				continue
			}
			if err := st.RequestConditionGap(ctx, id); err != nil {
				log.Printf("[xfer] request %s: %v", subject, err)
			}
		}
	}
}
