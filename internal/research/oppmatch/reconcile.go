// reconcile.go —— D-193：venue 悬空自检+缺口自动收口/复开（自 run.go 拆分，纯移动零语义改）。
package oppmatch

import (
	"context"
	"fmt"
	"log"
	"strings"

	"arb/internal/risk/gate"
	"arb/internal/store/pgstore"
)

// checkVenueFloat D-193①：venue 悬空自检——仅对已接执行轨模板（executor 映射集内），
// 其 credential 条件声明 venue 须落在执行轨 venue 集内；不符→warn alert 一次
// （episode 防抖）。未接执行轨模板声明新 venue=合法投资人请求不报。
// 内部不一致报系统不修数据（修种子=策略治理权）。
func checkVenueFloat(ctx context.Context, st *pgstore.Store, in []Template) {
	execVenue := gate.ExecTemplateVenues()
	for _, t := range in {
		ev, ok := execVenue[t.ID]
		if !ok {
			continue // 未接执行轨=不报
		}
		for _, c := range t.Conds {
			if c.Kind != "credential" {
				continue
			}
			ven := c.Subject
			if i := strings.Index(ven, ":"); i > 0 {
				ven = ven[:i]
			}
			if ven != ev {
				if err := st.InsertAlert(ctx, nil, "warn", fmt.Sprintf(
					"模板venue悬空：%s 声明凭据 %s，执行轨 venue=%s（需求行漂移，报系统不修数据）",
					t.ID, c.Subject, ev)); err != nil {
					log.Printf("[oppmatch] venue-float alert %s: %v", t.ID, err)
				}
			}
		}
	}
}

// reconcileGaps D-193②：缺口自动收口+复开——
// (a) 条件存在且满足→fulfilled；(b) 条件行已从模板集移除→fulfilled+note「需求已变更」；
// (c) fulfilled 行条件再失→回 requested（同 key 复用不插新行，episode 闸防抖照旧）；
// unknown 态（探针未就绪/传输层败）不收不复开。
func reconcileGaps(ctx context.Context, st *pgstore.Store, in []Template, view LedgerView) {
	condByKey := map[string]Cond{} // template|kind|subject → 当前模板条件集
	for _, t := range in {
		for _, c := range t.Conds {
			condByKey[t.ID+"|"+c.Kind+"|"+c.Subject] = c
		}
	}
	gaps, err := st.ListConditionGaps(ctx, "")
	if err != nil {
		log.Printf("[oppmatch] gap reconcile list: %v", err)
		return
	}
	for _, g := range gaps {
		if g.Status == "waived" {
			continue // 业主放弃线不动
		}
		if g.Kind == "runtime" {
			continue // D-196③：执行器运行期缺口豁免收口/复开（非模板条件 key）
		}
		key := g.TemplateID + "|" + g.Kind + "|" + g.Subject
		c, ok := condByKey[key]
		if !ok {
			// 条件行被删/模板停用→需求已变更收口（仅 open/requested）。
			if err := st.FulfillConditionGap(ctx, g.ID, "需求已变更"); err != nil {
				log.Printf("[oppmatch] gap fulfill(stale) %d: %v", g.ID, err)
			}
			continue
		}
		switch EvalCondTri(view, c) {
		case "ok": // 条件满足自动收口
			if err := st.FulfillConditionGap(ctx, g.ID, "条件满足自动收口"); err != nil {
				log.Printf("[oppmatch] gap fulfill %d: %v", g.ID, err)
			}
		case "miss": // fulfilled 行条件再失→复开（反向跳变防静默）
			if err := st.ReopenConditionGap(ctx, g.ID); err != nil {
				log.Printf("[oppmatch] gap reopen %d: %v", g.ID, err)
			}
		}
	}
}
