package posreview

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"nox/internal/platform/analyst"
	"nox/internal/store/pgstore"
)

// reviewerPrompt 复盘员 system：归因叙事+后续建议，数字只引用输入原值禁自算编造。
const reviewerPrompt = `你是公司复盘员。给定一笔已平仓持仓的判决书数字面（预期/实绩/成本/死因枚举），输出归因叙事与后续建议。

纪律：
1. 数字只能引用输入中的原值，禁止自行计算或编造任何数值。
2. exit_kind 是权威死因枚举，直接引用解释，禁止重新分类。
3. 只输出一行 JSON：{"review_note":"中文归因叙事（≤300字，含费蚀/滑点/偏差根因判断）","followup_action":"continue|reduce|quarantine|retire"}。
4. followup_action 是建议而非指令——continue=策略可按现参数续跑；reduce=建议缩额；quarantine=建议隔离观察；retire=建议淘汰（最终裁定归判据表）。
5. 不要输出 JSON 之外的任何文字。`

// narrative LLM 归因面：JSON schema 校验——违式/败调有限重试→llm_failed 标痕+warn alert。
// client nil→机器摘要 note（AI 未配=只落 Go 面，note 如实标注）。
func (d *Deps) narrative(ctx context.Context, client *analyst.Client, row pgstore.ClosedForReviewRow, r *pgstore.PositionReview) (note, action string, ok bool) {
	input := fmt.Sprintf("position_id=%d type=%s opportunity=%s\n"+
		"exit_kind=%s opened_at=%s closed_at=%s\n"+
		"expected_net_usd=%s actual_net_usd=%s\n"+
		"actual_cost_usd(fees)=%s fee_quality=%s",
		row.ID, row.Type, row.OpportunityID,
		empty(ptr(row.ExitKind)), ts(row.OpenedAt), ts(row.ClosedAt),
		f64(r.ExpectedNetUSD), f64(r.ActualNetUSD),
		f64(r.ActualCostUSD), empty(ptr(row.FeeQuality)))
	if client == nil {
		return fmt.Sprintf("pos#%d %s 平仓（%s）：预期净 %s，实绩净 %s，费 %s（无 AI——纯数字面）",
			row.ID, row.Type, empty(ptr(row.ExitKind)), f64(r.ExpectedNetUSD), f64(r.ActualNetUSD), f64(r.ActualCostUSD)), "", false
	}
	msgs := []analyst.Message{analyst.TextContent("user", input)}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "llm_failed: ctx", "", false
			case <-time.After(2 * time.Second):
			}
		}
		var out string
		if _, err := d.Run(ctx, client, msgs, reviewerPrompt, analyst.LoopCallbacks{
			OnDelta: func(t string) { out += t },
		}); err != nil {
			lastErr = err
			continue
		}
		n, a, err := parseVerdict(out)
		if err != nil {
			lastErr = err
			continue
		}
		return n, a, true
	}
	d.warn(ctx, fmt.Sprintf("复盘员 LLM 败（pos#%d）: %v——note 标 llm_failed", row.ID, lastErr))
	return fmt.Sprintf("llm_failed: %v（数字面照常——exit=%s 预期=%s 实绩=%s 费=%s）",
		lastErr, empty(ptr(row.ExitKind)), f64(r.ExpectedNetUSD), f64(r.ActualNetUSD), f64(r.ActualCostUSD)), "", false
}

// parseVerdict 判决书 JSON 面校验：note 非空 + action∈白名单，缺一判违式。
func parseVerdict(out string) (note, action string, err error) {
	out = strings.TrimSpace(out)
	if i := strings.Index(out, "{"); i >= 0 {
		if j := strings.LastIndex(out, "}"); j > i {
			out = out[i : j+1]
		}
	}
	var v struct {
		Note   string `json:"review_note"`
		Action string `json:"followup_action"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		return "", "", fmt.Errorf("verdict JSON 解败: %w", err)
	}
	if v.Note == "" {
		return "", "", fmt.Errorf("verdict 缺 review_note")
	}
	if !followupActions[v.Action] {
		return "", "", fmt.Errorf("followup_action=%q 非法枚举", v.Action)
	}
	return v.Note, v.Action, nil
}

func ptr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func f64(p *float64) string {
	if p == nil {
		return "NULL"
	}
	return fmt.Sprintf("%.4f", *p)
}

func ts(p *time.Time) string {
	if p == nil {
		return "NULL"
	}
	return p.Format("2006-01-02 15:04")
}
