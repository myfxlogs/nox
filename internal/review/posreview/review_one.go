package posreview

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"nox/internal/platform/analyst"
	"nox/internal/store/pgstore"
)

var errNilResolve = errors.New("resolve 未装配")

// followupActions 钦定枚举（schema 校验白名单——违式判 llm_failed 不静默收）。
var followupActions = map[string]bool{
	"continue": true, "reduce": true, "quarantine": true, "retire": true,
}

// ReviewOne 单仓判决书：Go 数字面先行，LLM 叙事面后补（nil client=只落 Go 面）。
func (d *Deps) ReviewOne(ctx context.Context, client *analyst.Client, row pgstore.ClosedForReviewRow) error {
	r := &pgstore.PositionReview{PositionID: row.ID, ReviewedAt: d.now()}

	// 假设引=机会 id（判决书的「当时判断」锚）。
	if row.OpportunityID != "" {
		r.HypothesisID = &row.OpportunityID
	}
	// ① 预期净=expected_net_bps×notional/1e4（双参缺一→NULL 禁外推）。
	if row.ExpectedNetBPS != nil && row.NotionalUSD != nil {
		exp := *row.ExpectedNetBPS * *row.NotionalUSD / 1e4
		r.ExpectedNetUSD = &exp
	}
	// ② 实绩=settlement.net_realized_usd——pnl_quality≠actual →NULL+质量痕。
	var quality []string
	if set, err := d.Store.GetSettlementByPos(ctx, row.ID); err == nil && set != nil {
		if set.PnlQuality == "actual" && set.NetRealizedUsd != nil {
			r.ActualNetUSD = set.NetRealizedUsd
		} else {
			quality = append(quality, fmt.Sprintf("实绩质量 %s→NULL", empty(set.PnlQuality)))
		}
	} else if err != nil {
		quality = append(quality, "settlement 读败")
	} else {
		quality = append(quality, "settlement 缺席")
	}
	// ③ 成本面：actual_cost=positions.fees_usd（venue 直记账面）；expected_cost
	// 无机读预期源→NULL（禁估）；两侧齐才落偏差。
	if row.FeesUSD != nil {
		r.ActualCostUSD = row.FeesUSD
	}
	if r.ExpectedCostUSD != nil && r.ActualCostUSD != nil {
		dev := *r.ActualCostUSD - *r.ExpectedCostUSD
		r.CostDeviationUSD = &dev
	}
	// ④ 风险面：expected/actual risk 无机读源→NULL。
	if row.FeeQuality != nil && *row.FeeQuality != "actual" {
		quality = append(quality, fmt.Sprintf("费质量 %s", *row.FeeQuality))
	}

	// ⑤ LLM 归因叙事（client nil→纯数字面）。
	note, action, llmOK := d.narrative(ctx, client, row, r)
	r.ReviewNote = &note
	if llmOK {
		r.FollowupAction = &action
	}
	if len(quality) > 0 {
		r.ReviewNote = strPtr(note + "【质量痕】" + strings.Join(quality, "；"))
	}
	return d.Store.UpsertPositionReview(ctx, r)
}

func (d *Deps) warn(ctx context.Context, msg string) {
	if err := d.Store.InsertAlertDedup(ctx, "warn", msg, 24*time.Hour); err != nil {
		log.Printf("[posreview] warn alert 落行败: %v", err)
	}
}

func strPtr(s string) *string { return &s }

func empty(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
