// idle_gap.go —— okx idle 资金扫描缺口（STATE 行）：cash_mgmt/idle 扫描径扩
// 非 binance 的 CEX 池入评估面。binance 有 earn 提案→执行径（goEarn）；
// okx unified 池（fin_venue_account_snapshot 现成行）余额闲置但无 earn 申购
// 执行面——如实记 fin_condition_gap 缺口行（executor-missing 类）+请求箱推，
// 不臆造 venue API。gate/其他 CEX 池同规则自动覆盖（earn 执行面白名单单源）。
package oppmatch

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"nox/internal/store/pgstore"
)

// earnExecVenues earn 提案可执行 venue 面（dciexec.goEarn 现径=binance 唯一；
// 新 venue 接上执行径时在此补名——缺口扫描自动收口不再报）。
var earnExecVenues = map[string]bool{"binance": true}

// idleGapScan 闲置 CEX 池缺口扫描（RunOnce 扫描拍尾挂）：CEX 池（非 mt 非 ghost）
// Idle=true 且权益>0 且 venue 无 earn 执行径→OpenConditionGap+RequestConditionGap
// （幂等——同键在册/已 requested 零重推）。读败仅 log 不阻塞扫描拍。
func idleGapScan(ctx context.Context, st *pgstore.Store) {
	pools, err := st.ExplorerIdleFunds(ctx, time.Now())
	if err != nil {
		log.Printf("[oppmatch] idle gap pools: %v", err)
		return
	}
	for _, p := range pools {
		if p.Ghost || p.VenueType == "mt4" || p.VenueType == "mt5" ||
			earnExecVenues[p.Venue] || !p.Idle || p.Equity <= 0 {
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
}
