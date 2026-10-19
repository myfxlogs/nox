// wire_pipeline_watch.go —— 管线堵点自盯 collector（PraisonAI 对照评估 §6.1③，
// §6.3 裁定序首件）。发现漏斗三段（假设等裁/提案等裁/毕业评审）「单段积压>阈
// 且 最老停滞>阈」→ warn 告警 dedup 6h（state-audit 呆行件同暴露模式）。
// 挂载=collect.Named 24h 拍（retention janitor 同款：零 facts、失败留痕不阻断、
// boot 首拍即出信号）。阈值 app_config 可覆写：pipeline_backlog_warn（缺省 10）/
// pipeline_stall_days_warn（缺省 7）——判据零字面量纪律：键可改，缺省仅代码兜底。
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"nox/internal/data/fact"
	"nox/internal/store/pgstore"
)

const (
	pipelineBacklogWarnDefault   = int64(10)
	pipelineStallDaysWarnDefault = int64(7)
	pipelineAlertDedup           = 6 * time.Hour
)

// funnelStageNames 段显示名（键=pgstore PipelineFunnelBacklog stage）。
var funnelStageNames = map[string]string{
	"hypothesis_ruling": "假设等裁决（memo）",
	"proposal_ruling":   "提案等裁决",
	"graduation_eval":   "毕业评审",
}

type pipelineWatchCollector struct{ st *pgstore.Store }

func (c *pipelineWatchCollector) Kind() string { return "pipeline_watch" }

// Poll 三段堵点扫描：阈值读取败=整轮失败交调度器退避重试（判据面坏不静默降缺省）；
// 单段告警插入败=留痕续扫（一批告警互不牵连）。
func (c *pipelineWatchCollector) Poll(ctx context.Context) ([]fact.Fact, error) {
	backlogWarn, stallDays, err := c.thresholds(ctx)
	if err != nil {
		return nil, err
	}
	loads, err := c.st.PipelineFunnelBacklog(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	for _, l := range pipelineBreaches(loads, backlogWarn, stallDays, now) {
		days := now.Sub(*l.Oldest).Hours() / 24
		msg := fmt.Sprintf("[管线堵点] %s %s：积压 %d 行（阈 %d）、最老停滞 %.1f 天（阈 %d 天）",
			funnelStageNames[l.Stage], l.Stage, l.Count, backlogWarn, days, stallDays)
		if err := c.st.InsertAlertDedup(ctx, "warn", msg, pipelineAlertDedup); err != nil {
			log.Printf("[pipeline_watch] %s 告警插入: %v", l.Stage, err)
		}
	}
	return nil, nil
}

// thresholds app_config 阈值读取（键缺/值≤0→代码缺省；读败→显式失败不降级）。
func (c *pipelineWatchCollector) thresholds(ctx context.Context) (backlogWarn, stallDays int64, err error) {
	backlogWarn, stallDays = pipelineBacklogWarnDefault, pipelineStallDaysWarnDefault
	if v, ok, err := c.st.AppConfigInt64(ctx, "pipeline_backlog_warn"); err != nil {
		return 0, 0, fmt.Errorf("pipeline_watch: pipeline_backlog_warn: %w", err)
	} else if ok && v > 0 {
		backlogWarn = v
	}
	if v, ok, err := c.st.AppConfigInt64(ctx, "pipeline_stall_days_warn"); err != nil {
		return 0, 0, fmt.Errorf("pipeline_watch: pipeline_stall_days_warn: %w", err)
	} else if ok && v > 0 {
		stallDays = v
	}
	return backlogWarn, stallDays, nil
}

// pipelineBreaches 堵点判定（纯函数便锚测）：积压>阈 且 最老停滞>阈，均严格大于
// （等值不触发）；零行段/无时戳段不判。
func pipelineBreaches(loads []pgstore.FunnelStageLoad, backlogWarn, stallDays int64, now time.Time) []pgstore.FunnelStageLoad {
	var out []pgstore.FunnelStageLoad
	for _, l := range loads {
		if l.Count <= backlogWarn || l.Oldest == nil {
			continue
		}
		if now.Sub(*l.Oldest) <= time.Duration(stallDays)*24*time.Hour {
			continue
		}
		out = append(out, l)
	}
	return out
}
