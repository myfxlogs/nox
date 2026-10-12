// report_cex_skips.go —— D-051③：日报 CEX 纸面入口拦截分布行（从 report.go 拆出，
// 文件行数闸）。环比差分——计数器进程内累计对 app_config 基线 JSON 快照。
package selfwatch

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// cexSkipReasonHuman 拦截原因内部键→运营话术（日报分布单点；未知键归「其他」）。
var cexSkipReasonHuman = map[string]string{
	"depth_missing": "深度无样本",
	"depth_low":     "深度不足",
	"funding_low":   "息费不足",
	"quote_dead":    "报价断流",
	"cooldown":      "冷却中",
}

// cexSkipLine CEX 纸面入口拦截分布行（环比差分——计数器进程内累计，
// app_config 基线 JSON 快照；重启后差分为负按当前值示，诚实不臆造）。
func (d Deps) cexSkipLine(ctx context.Context) (string, error) {
	cur := d.CexSkipStats()
	prev := map[string]int{}
	if v, ok, err := d.Store.AppConfigString(ctx, cfgCexSkipsPrev); err != nil {
		return "", fmt.Errorf("cex skips prev load: %w", err)
	} else if ok {
		if jerr := json.Unmarshal([]byte(v), &prev); jerr != nil {
			return "", fmt.Errorf("cex skips prev parse: %w", jerr)
		}
	}
	delta := map[string]int{}
	for k, c := range cur {
		diff := c - prev[k]
		if diff < 0 {
			diff = c // 进程重启计数清零——当前累计即本段真实值
		}
		if diff > 0 {
			delta[k] = diff
		}
	}
	j, err := json.Marshal(cur)
	if err != nil {
		return "", fmt.Errorf("cex skips marshal: %w", err)
	}
	if err := d.Store.UpsertAppConfig(ctx, cfgCexSkipsPrev, string(j), "selfwatch cex paper skip baseline"); err != nil {
		return "", fmt.Errorf("cex skips prev save: %w", err)
	}
	// 固定序五闸因 + 其他合并（keys 确定性）。
	order := []string{"depth_missing", "depth_low", "funding_low", "quote_dead", "cooldown"}
	shown := map[string]bool{}
	parts := []string{}
	other := 0
	for _, k := range order {
		shown[k] = true
		parts = append(parts, fmt.Sprintf("%s %d", cexSkipReasonHuman[k], delta[k]))
	}
	for k, v := range delta {
		if !shown[k] {
			other += v
		}
	}
	parts = append(parts, fmt.Sprintf("其他 %d", other))
	return "纸面 CEX 开仓拦截：" + strings.Join(parts, "｜") + "\n", nil
}
