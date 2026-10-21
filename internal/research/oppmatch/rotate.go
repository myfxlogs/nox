// rotate.go —— D-612/D-613 息差轮动扫描（并入 RunOnce 扫描拍，零新定时器）。
// 所内面：earn 池（venue_type='earn' 已部署仓）有鲜 APR 行+权益≥阈+持有窗出窗
// →earn_rotation 提案入 autoexec 族（执行拍实拉产品面/在册做净值差终判——
// 比价表值只做候选排序不充判定，D-613 补正①）。
// 跨 venue 面在 xfer.go（idle 门→低效仓门扩面+集中度透出）。
package oppmatch

import (
	"context"
	"fmt"
	"log"
	"time"

	"nox/internal/store/pgstore"
)

// rotateScan 所内轮动提案扫描：earn 池低效候选→earn_rotation 提案+评估卡。
// 提案幂等（template+venue proposed 唯一锚——同 venue 窗内零重开）；
// 无鲜 APR 行/读败 fail-closed 不比。
func rotateScan(ctx context.Context, st *pgstore.Store) {
	minUSD := 100.0
	if v, ok, err := st.AppConfigFloat(ctx, "rotation_min_usd"); err == nil && ok && v > 0 {
		minUSD = v
	}
	minHoldH := 168.0
	if v, ok, err := st.AppConfigFloat(ctx, "rotation_min_hold_hours"); err == nil && ok && v > 0 {
		minHoldH = v
	}
	aprs, err := st.LatestCexEarnAPRs(ctx)
	if err != nil {
		log.Printf("[rotate] earn apr 读败不比: %v", err)
		return
	}
	pools, err := st.ExplorerIdleFunds(ctx, time.Now())
	if err != nil {
		log.Printf("[rotate] pools 读败不比: %v", err)
		return
	}
	// venue→鲜 APR（≤24h+flexible+促销档剔除——tier_apr 促销件只存不入判据，
	// D-514 窗口教训；APR 列=基础档真值）。
	fresh := map[string]float64{}
	for _, r := range aprs {
		if r.Ccy != "USDT" || r.ProductType != "flexible" || time.Since(r.Ts) > 24*time.Hour {
			continue
		}
		if r.APR > fresh[r.Venue] {
			fresh[r.Venue] = r.APR
		}
	}
	for _, p := range pools {
		if p.Ghost || p.VenueType != "earn" || p.Currency != "USDT" ||
			p.Equity < minUSD || !execVenueSets(ctx, st)["earn_rotation"][p.Venue] { // D-651 投影
			continue
		}
		apr, ok := fresh[p.Venue]
		if !ok {
			continue // 比价源不鲜 fail-closed
		}
		// 最小持有窗：venue 最近 EARN 账本行在窗内 → 本轮不产提案。
		if ts, has, aerr := st.LatestEarnActivityAt(ctx, p.Venue); aerr != nil {
			log.Printf("[rotate] %s earn 活动史读败跳过: %v", p.Venue, aerr)
			continue
		} else if has && time.Since(ts) < time.Duration(minHoldH)*time.Hour {
			continue
		}
		pid, _, err := st.CreateProposal(ctx, &pgstore.Proposal{
			TemplateID: "earn_rotation", ExecTrack: "auto", Venue: p.Venue,
			Params: map[string]any{
				"ccy": "USDT", "est_amount": p.Equity, "table_apr": apr,
				"source": "rotation"}, // D-613 顺手锚：归因来源标=rotation
		})
		if err != nil {
			log.Printf("[rotate] proposal %s: %v", p.Venue, err)
			continue
		}
		// 评估卡（M4.1 无卡拒单）——gross=venue 鲜 APR 档（排序用；终判=执行拍实拉）。
		r := Result{TemplateID: "earn_rotation", ExecTrack: "auto"}
		r.APR = &apr
		r.GrossEvidence = fmt.Sprintf(
			"fin_cex_earn_apr %s USDT 鲜行 %.2f%%；earn 池在册 %.4g（轮动净值差终判=执行拍实拉）",
			p.Venue, apr*100, p.Equity)
		card := evalProposal(ctx, st, r, pid)
		if _, uerr := st.UpsertOppEval(ctx, card); uerr != nil {
			log.Printf("[rotate] eval card #%d: %v", pid, uerr)
		}
	}
}
