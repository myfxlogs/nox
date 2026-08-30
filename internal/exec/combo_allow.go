package exec

import (
	"strconv"
	"strings"
)

// ComboAllowSpec D-159 W1a R1：精确组合门禁——单条允许的 SWAPARB 组合规格。
// venueKey 格式 "account:mt4" / "account:mt5"（与 broker_accounts.venue + platform 后缀一致）。
// MaxLongQty/MaxShortQty/MaxGrossExposureUSD ≤0 = 该维度不限。
//
// 定义在 exec 包（最底层无依赖）：dashboard/autotrade 复用，避免循环依赖。
type ComboAllowSpec struct {
	Canonical           string
	VenueLong           string // 买腿 venueKey（account:mt4 或 account:mt5）
	VenueShort          string // 卖腿 venueKey
	MaxLongQty          float64
	MaxShortQty         float64
	MaxGrossExposureUSD float64
}

// ParseComboAllow 解析 ARB_SWAPARB_ALLOW_COMBOS（分号分隔记录，
// 每条 `canonical|venueLong|venueShort|maxLongQty|maxShortQty|maxGrossExposureUSD`）。
// 空字符串 → 返回 nil（RUNNING 下 fail-closed，由 gate 层判定）。
// 解析失败的条目跳过（不阻断其他条目）。
func ParseComboAllow(raw string) []ComboAllowSpec {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out []ComboAllowSpec
	for _, rec := range strings.Split(raw, ";") {
		rec = strings.TrimSpace(rec)
		if rec == "" {
			continue
		}
		fields := strings.Split(rec, "|")
		if len(fields) != 6 {
			continue
		}
		canonical := strings.TrimSpace(fields[0])
		venueLong := strings.TrimSpace(fields[1])
		venueShort := strings.TrimSpace(fields[2])
		if canonical == "" || venueLong == "" || venueShort == "" {
			continue
		}
		maxLong, ok1 := parseComboFloatField(fields[3])
		maxShort, ok2 := parseComboFloatField(fields[4])
		maxGross, ok3 := parseComboFloatField(fields[5])
		if !ok1 || !ok2 || !ok3 {
			continue
		}
		out = append(out, ComboAllowSpec{
			Canonical:           canonical,
			VenueLong:           venueLong,
			VenueShort:          venueShort,
			MaxLongQty:          maxLong,
			MaxShortQty:         maxShort,
			MaxGrossExposureUSD: maxGross,
		})
	}
	return out
}

// parseComboFloatField 解析组合字段中的 float（空串=0=不限）。
func parseComboFloatField(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, true
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// MatchComboAllow 检查 intent (canonical, venueBuy, venueSell) 是否在 allowlist 中。
// 返回匹配的 spec（nil = 不匹配）。
func MatchComboAllow(allow []ComboAllowSpec, canonical, venueBuy, venueSell string) *ComboAllowSpec {
	for i := range allow {
		a := &allow[i]
		if a.Canonical == canonical && a.VenueLong == venueBuy && a.VenueShort == venueSell {
			return a
		}
	}
	return nil
}
