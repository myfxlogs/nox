// idle_gap.go —— 闲置 CEX 池扫描（okx idle 缺口行+earn 多 venue 化两票并径）：
// Idle 池∈earnExecVenues（binance/okx/gate rail 面）→venue 键 cash_mgmt 提案；
// 面外 venue→fin_condition_gap executor 缺口+请求箱推（不臆造 venue API）。
// <$100 尘额两径都不入（尘池骚扰收益不抵）。
package oppmatch

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"nox/internal/store/pgstore"
)

// earnExecVenues earn 提案可执行 venue 面（dciexec.goEarn rail 化：binance/okx/gate
// 三径；新 venue 接上执行径时在此补名——缺口扫描自动收口不再报）。
var earnExecVenues = map[string]bool{"binance": true, "okx": true, "gate": true}

// idleGapDustUSD 粉尘阈：闲置权益 <$100 不出缺口行（okx:earn $169 类尘额
// 手工 waived 实证——尘池缺口骚扰收益不抵）。
const idleGapDustUSD = 100.0

// idleGapScan 闲置 CEX 池缺口扫描（RunOnce 扫描拍尾挂）：CEX 池（非 mt 非 ghost）
// Idle=true 且权益>0 且 venue 无 earn 执行径→OpenConditionGap+RequestConditionGap
// （幂等——同键在册/已 requested 零重推）。读败仅 log 不阻塞扫描拍。
func idleGapScan(ctx context.Context, st *pgstore.Store) {
	pools, err := st.ExplorerIdleFunds(ctx, time.Now())
	if err != nil {
		log.Printf("[oppmatch] idle gap pools: %v", err)
		return
	}
	cashLive := map[string]bool{} // D-523⑥：本轮仍持款的 cash_mgmt venue 集
	for _, p := range pools {
		if p.Ghost || p.VenueType == "mt4" || p.VenueType == "mt5" ||
			!p.Idle || p.Equity < idleGapDustUSD {
			continue
		}
		// 有执行径的闲池（earnExecVenues 面）→直接产 cash_mgmt 提案（pending 唯一锚
		// 1024 含 venue——同模板异所各行其位幂等）。USDT 先行域（earn 提案定值域）。
		if earnExecVenues[p.Venue] {
			if p.Currency != "USDT" {
				continue
			}
			cashLive[p.Venue] = true
			pid, _, err := st.CreateProposal(ctx, &pgstore.Proposal{
				TemplateID: "cash_mgmt", ExecTrack: "auto", Venue: p.Venue})
			if err != nil {
				log.Printf("[oppmatch] idle proposal %s: %v", p.Venue, err)
				continue
			}
			// 提案须带评估卡（M4.1 无卡拒单——SG 实证 #377/#378 零卡死提案）。
			// 证源=池闲置实证+venue earn APR 最新行；APR 缺→卡面 gross missing
			// 如实 unevaluable 不编值。
			r := Result{TemplateID: "cash_mgmt", ExecTrack: "auto"}
			if apr, ok, aerr := st.LatestEarnAPR(ctx, p.Venue, p.Currency); aerr == nil && ok {
				r.APR = &apr
				r.GrossEvidence = fmt.Sprintf(
					"fin_cex_earn_apr %s %s 最新行；闲置池实证 equity=%.4g 30d收益=0",
					p.Venue, p.Currency, p.Equity)
			}
			card := evalProposal(ctx, st, r, pid)
			if _, uerr := st.UpsertOppEval(ctx, card); uerr != nil {
				log.Printf("[oppmatch] idle eval card #%d: %v", pid, uerr)
			}
			continue
		}
		subj := p.Venue + ":" + p.VenueType
		id, err := st.OpenConditionGap(ctx, &pgstore.ConditionGap{
			TemplateID: "cash_mgmt", Kind: "executor", Subject: subj,
			RequestText: fmt.Sprintf(
				"%s 池闲置 %.4g %s（equity %.4g，30d 收益=0）——%s 无 earn 申购执行径；"+
					"请 venue 实查理财写面并立票接径，或人工划转至可承接 venue", p.Venue, p.Balance,
				strings.ToUpper(p.Currency), p.Equity, subj),
		})
		if err != nil {
			log.Printf("[oppmatch] idle gap open %s: %v", subj, err)
			continue
		}
		if err := st.RequestConditionGap(ctx, id); err != nil {
			log.Printf("[oppmatch] idle gap request %s: %v", subj, err)
		}
	}
	// D-523⑥：目标款已消失（池不再闲/权益<尘阈）→同拍 expired 不挂 SLA；
	// 决策面由 expired 态如实提示（existed-but-dead 提案不再占待裁面）。
	liveVenues := make([]string, 0, len(cashLive))
	for v := range cashLive {
		liveVenues = append(liveVenues, v)
	}
	if n, err := st.ExpireProposalsOutsideVenues(ctx, "cash_mgmt", liveVenues); err != nil {
		log.Printf("[oppmatch] cash target expire: %v", err)
	} else if n > 0 {
		log.Printf("[oppmatch] cash_mgmt target gone → expired %d proposals", n)
	}
}
