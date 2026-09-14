// parse_store.go push2 API 响应解析+目标品种过滤+fin_repo_rates 写入。
// 从 collector.go 拆出（D-275 纯重构）。
package repo

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"time"
)

// parseAndStore 解析 JSON，过滤目标四品种，写 fin_repo_rates。
// 脏值防御：f2<=0 或缺字段 → 跳过该品种写入+log（设计 §2.1 R2）。
func (c *RepoCollector) parseAndStore(ctx context.Context, body []byte, now time.Time) ([]RepoRateRow, error) {
	var resp push2Response
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("json unmarshal: %w", err)
	}
	tradeDate := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, c.location)
	var stored []RepoRateRow
	for _, raw := range resp.Data.Diff {
		var item diffItem
		if err := json.Unmarshal(raw, &item); err != nil {
			log.Printf("repo: parse diff item: %v（跳过）", err)
			continue
		}
		if !targetCodes[item.F12] {
			continue // 非目标品种
		}
		// f2/f1 灵活解析：ratePct = f2 / 10^f1（东方财富生产页面 JS 实证，核验依据 §1）
		rateRaw, ok := parseFlexFloat(item.F2)
		if !ok || rateRaw <= 0 {
			// 脏值防御：f2<=0 或缺字段 → 跳过+log（设计 §2.1 R2）
			log.Printf("repo: %s f2 invalid (%s), skip", item.F14, string(item.F2))
			continue
		}
		ratePrec, ok := parseFlexFloat(item.F1)
		if !ok {
			log.Printf("repo: %s f1 missing/invalid (%s), skip", item.F14, string(item.F1))
			continue
		}
		if !validPrecision(ratePrec) {
			log.Printf("repo: %s f1 precision out of range 0..6 (%v), skip", item.F14, ratePrec)
			continue
		}
		ratePct := rateRaw / math.Pow(10, ratePrec)

		// f3/f152 涨跌幅：changePct = f3 / 10^f152（东方财富生产页面 JS 实证）
		// 涨跌幅或其精度缺失/无效 → 保留利率，change_pct 写 NULL（不写假零）
		var changePct *float64
		if changeRaw, ok := parseFlexFloat(item.F3); ok {
			if changePrec, ok := parseFlexFloat(item.F152); ok && validPrecision(changePrec) {
				v := changeRaw / math.Pow(10, changePrec)
				changePct = &v // 合法 f3=0 → &0.0（真实零，非缺失）
			} else {
				log.Printf("repo: %s f152 precision missing/invalid (%s), change_pct=NULL", item.F14, string(item.F152))
			}
		} else {
			log.Printf("repo: %s f3 missing/invalid (%s), change_pct=NULL", item.F14, string(item.F3))
		}

		if err := c.store.InsertRepoRate(ctx, item.F12, item.F14, ratePct, changePct, tradeDate); err != nil {
			return nil, fmt.Errorf("insert %s: %w", item.F14, err)
		}
		stored = append(stored, RepoRateRow{Code: item.F12, Name: item.F14, RatePct: ratePct})
	}
	return stored, nil
}

// RepoRateRow 是已存储的利率行（供告警判定用）。
type RepoRateRow struct {
	Code    string
	Name    string
	RatePct float64
}

// parseFlexFloat 灵活解析 f2/f152：接受数字（int/float）与字符串两形态。
// "-" / null / 缺失 → (0, false)。
func parseFlexFloat(raw json.RawMessage) (float64, bool) {
	if len(raw) == 0 || string(raw) == "null" || string(raw) == `"-"` {
		return 0, false
	}
	// 尝试数字
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return f, true
	}
	// 尝试字符串数字
	var s string
	if err := json.Unmarshal(raw, &s); err == nil && s != "" && s != "-" {
		var f2 float64
		if _, err := fmt.Sscanf(s, "%f", &f2); err == nil {
			return f2, true
		}
	}
	return 0, false
}

// validPrecision 校验精度参数是否在有效整数范围 0..6。
// 东方财富精度参数为小数位数，超出范围视为脏值不猜测单位。
func validPrecision(p float64) bool {
	if p != math.Trunc(p) { // 非整数
		return false
	}
	return p >= 0 && p <= 6
}
