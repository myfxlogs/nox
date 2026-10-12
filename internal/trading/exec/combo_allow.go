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

// ParseComboAllow 解析 NOX_SWAPARB_ALLOW_COMBOS（分号分隔记录，
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

// XAssetComboAllowSpec D-311 S6：XASSET_CARRY 精确组合门禁——跨 canonical 零信任白名单。
// 区别于 SWAPARB：CanonicalBuy/CanonicalSell 各自独立（买腿/卖腿可不同 canonical）。
// venueKey 用 account key 格式（"3001157:mt5"/"gate:perp"，非 EXR 别名——机会行实测口径）。
// MaxLongQty/MaxShortQty/MaxGrossUSD ≤0 = 该维度不限。
type XAssetComboAllowSpec struct {
	CanonicalBuy  string
	CanonicalSell string
	VenueBuy      string // 买腿 venueKey（account:mt4/mt5 或 cex:perp）
	VenueSell     string // 卖腿 venueKey
	MaxLongQty    float64
	MaxShortQty   float64
	MaxGrossUSD   float64
	NotionalUSD   *float64 // D-367 S3：组合级 sizing 名义额；nil=回退全局 env（表来源映射；env 来源恒 nil）
	MarginUSD     *float64 // D-010：组合保证金总预算（双腿占用和；与 NotionalUSD 互斥）
}

// ParseXAssetComboAllow 解析 NOX_XASSET_ALLOW_COMBOS（分号分隔记录，每条
// `canonicalBuy|canonicalSell|venueBuy|venueSell|maxLongQty|maxShortQty|maxGrossUSD`，七段）。
// 空字符串 → 返回 nil（XASSET_CARRY 全拒——零信任显式白名单语义，调用方无条件检查）。
// 解析失败的条目跳过（不阻断其他条目）。
func ParseXAssetComboAllow(raw string) []XAssetComboAllowSpec {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out []XAssetComboAllowSpec
	for _, rec := range strings.Split(raw, ";") {
		rec = strings.TrimSpace(rec)
		if rec == "" {
			continue
		}
		fields := strings.Split(rec, "|")
		if len(fields) != 7 {
			continue
		}
		canonicalBuy := strings.TrimSpace(fields[0])
		canonicalSell := strings.TrimSpace(fields[1])
		venueBuy := strings.TrimSpace(fields[2])
		venueSell := strings.TrimSpace(fields[3])
		if canonicalBuy == "" || canonicalSell == "" || venueBuy == "" || venueSell == "" {
			continue
		}
		maxLong, ok1 := parseComboFloatField(fields[4])
		maxShort, ok2 := parseComboFloatField(fields[5])
		maxGross, ok3 := parseComboFloatField(fields[6])
		if !ok1 || !ok2 || !ok3 {
			continue
		}
		out = append(out, XAssetComboAllowSpec{
			CanonicalBuy:  canonicalBuy,
			CanonicalSell: canonicalSell,
			VenueBuy:      venueBuy,
			VenueSell:     venueSell,
			MaxLongQty:    maxLong,
			MaxShortQty:   maxShort,
			MaxGrossUSD:   maxGross,
		})
	}
	return out
}

// MatchXAssetComboAllow 检查 (canonicalBuy, canonicalSell, venueBuy, venueSell) 是否在
// XASSET allowlist 中。返回匹配的 spec（nil = 不匹配 → 调用方拒绝，空表全拒）。
func MatchXAssetComboAllow(allow []XAssetComboAllowSpec, canonicalBuy, canonicalSell, venueBuy, venueSell string) *XAssetComboAllowSpec {
	for i := range allow {
		a := &allow[i]
		if a.CanonicalBuy == canonicalBuy && a.CanonicalSell == canonicalSell &&
			a.VenueBuy == venueBuy && a.VenueSell == venueSell {
			return a
		}
	}
	return nil
}
