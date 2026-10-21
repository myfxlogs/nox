// dormant.go —— 休眠轨（dormant:<tmpl>@<venue>）venue 粒度判定/写面。
// 粒度钉死：@binance 键只压 binance 面（修：枚举前模板级短路曾把 binance
// 休眠误扩到全 venue=sell_high okx 提案面被掐）；全 venue 休眠才整模板短路。
package oppmatch

import (
	"context"
	"fmt"
	"log"

	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"
)

// venueSetFor 模板提案 venue 枚举面（dormant 判定与休眠写面同尺）：
// dci 腿族=DCIExecVenues 覆盖集；earn 族=EarnExecVenues（达标径内再按
// 有报价行 rail 细化）；其余模板=binance 单 venue 旧语义。
func venueSetFor(tid string) []string {
	if _, ok := gate.ExecTemplates[tid]; ok {
		return dciProposalVenues
	}
	if gate.EarnTemplates[tid] {
		return sortedVenues(gate.EarnExecVenues)
	}
	return []string{"binance"}
}

// dormantAll 扫描拍 dormant 判定点：枚举面逐 venue 查 dormant 键，
// 命中 venue 存量 proposed 翻 deferred+info 留痕（无条件——达标/缺口径同做）。
// 返回全休眠判定：true=整模板短路（零新提案零缺口请求）。
func dormantAll(ctx context.Context, st *pgstore.Store, dormant map[string]bool, tid string, vs []string) bool {
	all := len(vs) > 0
	for _, v := range vs {
		if dormant[tid+"@"+v] {
			deferDormant(ctx, st, tid, v)
		} else {
			all = false
		}
	}
	return all
}

// deferDormant 命中 venue 处置：存量 proposed→deferred（CAS 幂等）+info 台账留痕。
func deferDormant(ctx context.Context, st *pgstore.Store, tid, venue string) {
	if n, derr := st.DeferProposalsByPair(ctx, tid, venue); derr == nil && n > 0 {
		log.Printf("[oppmatch] 休眠轨 %s@%s 翻 deferred %d 行", tid, venue, n)
	}
	if err := st.InsertAlert(ctx, nil, "info", fmt.Sprintf(
		"休眠轨（D-191R7）%s@%s 休眠——本 venue 跳过提案", tid, venue)); err != nil {
		log.Printf("[oppmatch] dormant alert %s: %v", tid, err)
	}
}

// dormantWritePrecheck D-247 预检不过→休眠写面同 venue 粒度：枚举集逐 venue
// 落 dormant 键+存量翻 deferred（只写 @binance 会漏其他 venue 提案面）。
// vs 空（cash_mgmt 类 match 环不产提案模板）=零写零告警。
func dormantWritePrecheck(ctx context.Context, st *pgstore.Store, tid string, vs []string, reason string) {
	for _, v := range vs {
		if uerr := st.UpsertCapabilityConstraint(ctx,
			DormantConstraint(tid, v, reason)); uerr != nil {
			log.Printf("[oppmatch] dormant constraint %s@%s: %v", tid, v, uerr)
		}
		if n, derr := st.DeferProposalsByPair(ctx, tid, v); derr == nil && n > 0 {
			log.Printf("[oppmatch] 预检休眠 %s@%s 翻 deferred %d 行", tid, v, n)
		}
	}
	if len(vs) > 0 {
		if err := st.InsertAlert(ctx, nil, "info", fmt.Sprintf(
			"可行性预检不过（D-247）%s 休眠（%d venue）：%s", tid, len(vs), reason)); err != nil {
			log.Printf("[oppmatch] precheck alert %s: %v", tid, err)
		}
	}
}
