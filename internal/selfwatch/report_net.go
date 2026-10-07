// report_net.go —— D-033① 逐日净额快照差分行 + 日分桶行渲染（D-102 自 report.go
// 拆出守 300 行纪律；纯搬移零逻辑变更）。
package selfwatch

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"arb/internal/scan"
)

// dailyNetLine 逐日净额行：app_config 快照差分（prev 缺=首日基线记 0 增量并播种）。
func (d Deps) dailyNetLine(ctx context.Context, windowNet float64, now time.Time) (string, error) {
	day := now.In(bj).Format("2006-01-02")
	hist := map[string]float64{}
	if v, ok, err := d.Store.AppConfigString(ctx, cfgPaperNetHist); err != nil {
		return "", fmt.Errorf("net hist load: %w", err)
	} else if ok {
		if jerr := json.Unmarshal([]byte(v), &hist); jerr != nil {
			return "", fmt.Errorf("net hist parse: %w", jerr)
		}
	}
	hist[day] = windowNet
	// 滚动保留近 60 天（表行不无限长）。
	if len(hist) > 60 {
		keys := make([]string, 0, len(hist))
		for k := range hist {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys[:len(keys)-60] {
			delete(hist, k)
		}
	}
	buf, err := json.Marshal(hist)
	if err != nil {
		return "", fmt.Errorf("net hist marshal: %w", err)
	}
	if err := d.Store.UpsertAppConfig(ctx, cfgPaperNetHist, string(buf), "D-033 逐日净额快照（内部状态键）"); err != nil {
		return "", fmt.Errorf("net hist save: %w", err)
	}
	keys := make([]string, 0, len(hist))
	for k := range hist {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	var prev float64
	for i, k := range keys {
		dv := hist[k]
		if i == 0 {
			parts = append(parts, fmt.Sprintf("%s $%.4f", k[5:], dv))
		} else {
			parts = append(parts, fmt.Sprintf("%s %+.4f", k[5:], dv-prev))
		}
		prev = dv
	}
	return "逐日净额：" + strings.Join(parts, "｜") + "\n", nil
}

// joinBuckets 日分桶行渲染（"MM-DD ×N($sum)"；退出原因经 exitReasonHuman 中文化）。
func joinBuckets(bs []scan.PaperDayBucket, withReasons bool) string {
	parts := make([]string, 0, len(bs))
	for _, b := range bs {
		s := fmt.Sprintf("%s ×%d($%.4f)", b.Day, b.Rows, b.Sum)
		if withReasons && len(b.Reasons) > 0 {
			rs := make([]string, 0, len(b.Reasons))
			for _, r := range b.Reasons {
				rs = append(rs, exitReasonHuman(r))
			}
			s += "[" + strings.Join(rs, ",") + "]"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "｜")
}
