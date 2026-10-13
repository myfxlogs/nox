// lifecycle.go —— D-260 M4.2：扫描拍并入生命周期三件（零新定时器）：
// ① fin_strategy 覆盖（strategy_stats+模板键全登记）
// ② 提案 SLA：proposed 超龄→expired+critical 告警（期限=app_config proposal_sla_hours）
// ③ paper 态晋级评估：判据行（kind='graduation'）点火→graduated+「可晋级」告警
//
// 晋级≠真金：graduated 仍非 live（disabled 语义）；live 仅委员会显式迁移
// （SetStrategyState decided_by='committee'）——首次进 live 出提示告警不自动执行。
package oppmatch

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"nox/internal/portfolio/lifecycle"
	"nox/internal/store/pgstore"
)

// runLifecycle 并入 RunOnce 扫描拍。失败单项 log 不中断扫描（拍幂等可重跑）。
func runLifecycle(ctx context.Context, st *pgstore.Store, templates []Template) {
	tids := make([]string, 0, len(templates))
	for _, t := range templates {
		tids = append(tids, t.ID)
	}
	if n, err := st.EnsureStrategyRows(ctx, tids); err != nil {
		log.Printf("[lifecycle] ensure rows: %v", err)
	} else if n > 0 {
		log.Printf("[lifecycle] ensured %d fin_strategy rows", n)
	}
	proposalSLA(ctx, st)
	evaluateGraduation(ctx, st)
}

// proposalSLA 提案超龄→expired+critical（期限机读；键缺/非法→默认 24h 钦定回退+log）。
func proposalSLA(ctx context.Context, st *pgstore.Store) {
	slaH := 24.0
	v, ok, err := st.AppConfigFloat(ctx, "proposal_sla_hours")
	if err != nil {
		log.Printf("[lifecycle] proposal_sla_hours read: %v（默认 24h）", err)
	} else if ok && v > 0 {
		slaH = v
	}
	ids, err := st.ExpireProposalsOverAge(ctx, time.Duration(slaH*float64(time.Hour)))
	if err != nil {
		log.Printf("[lifecycle] proposal SLA expire: %v", err)
		return
	}
	if len(ids) == 0 {
		return
	}
	strs := make([]string, len(ids))
	for i, id := range ids {
		strs[i] = fmt.Sprintf("#%d", id)
	}
	log.Printf("[lifecycle] SLA expired proposals: %s", strings.Join(strs, ","))
	if err := st.InsertAlert(ctx, nil, "critical", fmt.Sprintf(
		"[提案SLA] %d 条 proposed 超龄 %.0fh 未裁决→expired：%s（重拍复评）",
		len(ids), slaH, strings.Join(strs, ","))); err != nil {
		log.Printf("[lifecycle] SLA alert: %v", err)
	}
}

// evaluateGraduation paper 态策略逐条按判据表评估；点火→graduated+可晋级告警。
// metrics=AggregateStrategyStats 实绩（零臆造源——判据 conds 引用无证 metric 恒 false）。
func evaluateGraduation(ctx context.Context, st *pgstore.Store) {
	names, err := st.ListStrategiesByState(ctx, "paper")
	if err != nil || len(names) == 0 {
		return
	}
	for _, name := range names {
		rows, err := st.ListStrategyCriteria(ctx, name, "graduation")
		if err != nil || len(rows) == 0 {
			continue
		}
		agg, err := st.AggregateStrategyStats(ctx, name)
		if err != nil {
			continue
		}
		metrics := map[string]float64{
			"total_trades": float64(agg.Total),
			"avg_pnl":      agg.AvgPnl,
			"median_pnl":   agg.MedianPnl,
		}
		if agg.Total > 0 {
			metrics["win_rate"] = float64(agg.Wins) / float64(agg.Total)
		}
		crits := make([]lifecycle.Criterion, 0, len(rows))
		for _, r := range rows {
			crits = append(crits, lifecycle.Criterion{Strategy: r.Strategy, Kind: r.Kind, Pred: r.Pred, Params: r.Params})
		}
		fire, hits := lifecycle.FireCriteria(metrics, crits)
		if !fire {
			continue
		}
		if err := st.SetStrategyState(ctx, name, "graduated", "system", hits); err != nil {
			log.Printf("[lifecycle] graduate %s: %v", name, err)
			continue
		}
		if err := st.InsertAlert(ctx, nil, "info", fmt.Sprintf(
			"[策略晋级] %s paper→graduated（判据命中 %s）——可晋级，真金授权待委员会",
			name, strings.Join(hits, "+"))); err != nil {
			log.Printf("[lifecycle] graduate alert %s: %v", name, err)
		}
	}
}
