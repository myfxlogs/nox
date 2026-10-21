// gap_capital_verify.go —— D-652-C capital 回执核验环：marked 行桶值锚核验。
// idle_capital_monitor 30min 拍搭车；读败 fail-closed（不翻 fulfilled 不告警刷）。
package dashboard

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"nox/internal/store/pgstore"
)

// markStaleWarn 悬空告警龄阈：marked 超此时长未实证翻 fulfilled → warn。
const markStaleWarn = 24 * time.Hour

// markTargetSlack 到账判据折扣：目标额×0.95 即算实到（容差吸价差/费率）。
const markTargetSlack = 0.95

var markBaseRe = regexp.MustCompile(`mark_base_usd=(-?[0-9.eE+-]+)`)

// parseMarkBase resolve_note 内嵌基线桶值（ResolveRequest 标注时点写）；
// 缺位=false→核验走绝对额判据（legacy 降标行无可回算基线）。
func parseMarkBase(note string) (float64, bool) {
	m := markBaseRe.FindStringSubmatch(note)
	if len(m) != 2 {
		return 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// bucketValueUSD 目标桶现值解算（target 三形）：
//   - "venue:vt:ccy" 精确桶（okx:unified:USDT）——该 bucket 行 equity；
//   - "venue:vt" 前缀——该 venue:type 下全币种桶 equity 合计；
//   - 裸币种（USDT/USD 等无 ':'）——全 venue 同币种桶 equity 合计（oppmatch
//     级 capital 条件的 CapitalBy 口径）。
//
// 零行命中=false（核验侧 fail-closed 不臆零）。
func bucketValueUSD(snaps []pgstore.CapitalPoolRow, target string) (float64, bool) {
	parts := strings.Split(target, ":")
	var sum float64
	var hit bool
	for _, r := range snaps {
		if r.Ghost || r.Stale {
			continue // 幽灵/陈行不进核验面（同 NAV 口径）
		}
		switch len(parts) {
		case 3:
			if r.Venue == parts[0] && r.VenueType == parts[1] &&
				strings.EqualFold(r.Currency, parts[2]) {
				sum, hit = sum+r.Equity, true
			}
		case 2:
			if r.Venue == parts[0] && r.VenueType == parts[1] {
				sum, hit = sum+r.Equity, true
			}
		case 1:
			if strings.EqualFold(r.Currency, target) {
				sum, hit = sum+r.Equity, true
			}
		}
	}
	return sum, hit
}

// verifyMarkedCapital capital marked 行核验拍：桶值≥基线+target×0.95（基线缺=
// 绝对额 cur≥target×0.95）→FulfillConditionGap 实证收口；marked_at+24h 未
// 实证→「回执悬空」warn（dedup 24h）。快照/行读败=整拍跳（fail-closed）。
func (m *IdleCapitalMonitor) verifyMarkedCapital(ctx context.Context) {
	db := m.svc.gradDB()
	if db == nil {
		return
	}
	gaps, err := db.ListMarkedCapitalGaps(ctx)
	if err != nil || len(gaps) == 0 {
		return
	}
	snaps, err := db.CapitalPoolSnapshots(ctx)
	if err != nil {
		return // 快照读败 fail-closed——不核验不告警（下拍重试）
	}
	for _, g := range gaps {
		if g.TargetUSD == nil || *g.TargetUSD <= 0 || g.TargetBucket == "" {
			continue // 无核验锚=作曲器未带列（新码后产出必有，存量零锚行不核验）
		}
		cur, ok := bucketValueUSD(snaps, g.TargetBucket)
		if !ok {
			continue // 桶读面零命中=读败同域 fail-closed
		}
		base, hasBase := parseMarkBase(g.ResolveNote)
		need := *g.TargetUSD * markTargetSlack
		if hasBase {
			need += base
		}
		if cur >= need {
			note := fmt.Sprintf("到账实测核验：%s 桶值 %.2f≥基线%.2f+目标%.2f×%.2f（回执实证收口）",
				g.TargetBucket, cur, base, *g.TargetUSD, markTargetSlack)
			if !hasBase {
				note = fmt.Sprintf("到账实测核验：%s 桶值 %.2f≥目标%.2f×%.2f（无基线绝对额判据）",
					g.TargetBucket, cur, *g.TargetUSD, markTargetSlack)
			}
			if err := db.FulfillConditionGap(ctx, g.ID, note); err == nil {
				log.Printf("[idle-capital] gap#%d marked→fulfilled（%s）", g.ID, note)
			}
			continue
		}
		if g.MarkedAt != nil && time.Since(*g.MarkedAt) > markStaleWarn {
			_ = db.InsertAlertDedup(ctx, "warn", fmt.Sprintf(
				"条件缺口#%d capital 回执悬空超 24h：%s %s —— 人工已标 fulfilled 但 %s 桶值实测 %.2f<目标 %.2f（钱未到账或量级不符，请实查划转）",
				g.ID, g.TemplateID, g.Subject, g.TargetBucket, cur, *g.TargetUSD), markStaleWarn)
		}
	}
}
