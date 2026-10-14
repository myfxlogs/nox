// swapscan.go —— CFD swap 面进研究管线：facts swap_long/swap_short（采集在跑
// 消费断档）→正息面排榜→fin_opp_hypothesis 假设行（S1 同径幂等 category_key）。
// 年化粗估=bps_daily×365/100（口径假设注明——bps 日率换 %/yr）；方向性如实标
// 裸 delta 敞口（同 DCI 批评面——非「无风险」叙事）。账户权益=0 的面标
// 「面在钱不在」不入榜（fin_venue_account_snapshot 最新池 equity 对拍）。
// 只产假设——不产提案不碰钱；幂等重跑零重推（rejected 在册抑重推=弃件记忆）。
package oppmatch

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"nox/internal/store/pgstore"
)

// swapFactFresh swap facts 鲜度窗（采集小时级——24h 无新行不递陈旧值）。
const swapFactFresh = 24 * time.Hour

// swapScan CFD swap 正息面扫描（RunOnce 扫描拍尾挂，零新定时器）。
func swapScan(ctx context.Context, st *pgstore.Store) {
	rows, err := st.LatestSwapFacts(ctx, swapFactFresh)
	if err != nil {
		log.Printf("[oppmatch] swap facts: %v", err)
		return
	}
	// venue 权益对拍（面在钱不在=equity≤0 的 MT 户如实标不递榜）。
	pools, perr := st.CapitalPoolSnapshots(ctx)
	eqByVenue := map[string]float64{}
	if perr == nil {
		for _, p := range pools {
			if p.VenueType == "mt4" || p.VenueType == "mt5" {
				eqByVenue[p.Venue] += p.Equity
			}
		}
	} else {
		log.Printf("[oppmatch] swap equity pools: %v（权益缺按面在钱不在处理——不递榜）", perr)
	}
	side := map[string]string{"swap_long": "long", "swap_short": "short"}
	var newKeys []string
	nofund := map[string]float64{} // venue→面数（权益 0 如实计）
	for _, r := range rows {
		if r.BpsDaily <= 0 {
			continue // 负息=付方不收（榜只列正息面）
		}
		s, ok := side[r.Kind]
		if !ok || r.Symbol == "" {
			continue
		}
		if eq, known := eqByVenue[r.Venue]; !known || eq <= 0 {
			nofund[r.Venue]++ // 面在钱不在——不入榜
			continue
		}
		ann := r.BpsDaily * 365 / 100 // bps_daily→%/yr 粗估（口径假设注明）
		key := fmt.Sprintf("cfd_swap|%s|%s|%s", r.Venue, r.Symbol, s)
		_, inserted, _, uerr := st.UpsertHypothesis(ctx, pgstore.OppHypothesis{
			Venue: r.Venue, CategoryKey: key,
			InstType: "cfd", Shape: "swap_carry",
			Sample: fmt.Sprintf("%s %s %s swap %+.2f bps/d", r.Venue, r.Symbol, s, r.BpsDaily),
			Detail: fmt.Sprintf("swap_bps_daily=%.4f（venue 原值） ann≈%.1f%%（bps×365/100 口径假设）；"+
				"裸方向 delta 敞口非无风险对冲——FX/商品 carry 面", r.BpsDaily, ann),
		})
		if uerr != nil {
			log.Printf("[oppmatch] swap hypothesis %s: %v", key, uerr)
			continue
		}
		if inserted {
			newKeys = append(newKeys,
				fmt.Sprintf("%s %s %.2fbps/d≈%.1f%%/yr", r.Symbol, s, r.BpsDaily, ann))
		}
	}
	if len(nofund) > 0 {
		var vs []string
		for v := range nofund {
			vs = append(vs, v)
		}
		sort.Strings(vs)
		if aerr := st.InsertAlertDedup(ctx, "info", fmt.Sprintf(
			"CFD swap 面在钱不在：%s（账户权益=0 或快照缺——面如实剔榜）",
			strings.Join(vs, ",")), 24*time.Hour); aerr != nil {
			log.Printf("[oppmatch] swap nofund alert: %v", aerr)
		}
	}
	if len(newKeys) > 0 {
		sort.Strings(newKeys)
		if aerr := st.InsertAlert(ctx, nil, "info", fmt.Sprintf(
			"CFD swap 正息面新假设 %d 条：%s（bps_daily×365/100 粗估；裸 delta 敞口如实标——只产假设不碰钱）",
			len(newKeys), strings.Join(newKeys, "；"))); aerr != nil {
			log.Printf("[oppmatch] swap hypothesis alert: %v", aerr)
		}
	}
}
