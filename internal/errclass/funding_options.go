// funding_options.go —— D-560 S2：资金拒文 → 可行解法行（纯函数，autotrade warn 与
// dashboard 拒事实透出共调=P3 单源；落 errclass 因 dashboard→autotrade 无白名单边）。
// 全部由拒文实数机械推导（need/avail/R/lev），缺字段即省该项不臆造；
// 非资金拒文（IsFundingRejectMsg 判否）→ ""。白话禁词：不出现 avail/lev/notional 原文词。
package errclass

import (
	"fmt"
	"math"
	"regexp"
	"strings"
)

var (
	rejectNeedRe  = regexp.MustCompile(`(?i)(?:need|required) ([\d.]+)`)
	rejectAvailRe = regexp.MustCompile(`(?i)avail ([\d.]+)`)
	rejectRatioRe = regexp.MustCompile(`× ([\d.]+)`)
	rejectLevRe   = regexp.MustCompile(`(?i)lev ([\d.]+)`)
	// RejectLegRe D-563 S5：拒文腿键提取（"leg gate:perp:"）——调用方解析账户名用。
	RejectLegRe = regexp.MustCompile(`(?i)\bleg (\S+?):`)
)

func rejectNum(re *regexp.Regexp, msg string) (float64, bool) {
	m := re.FindStringSubmatch(msg)
	if m == nil {
		return 0, false
	}
	var v float64
	if _, err := fmt.Sscanf(m[1], "%g", &v); err != nil {
		return 0, false
	}
	return v, true
}

func ceil50(v float64) int64 { return int64(math.Ceil(v/50) * 50) }

// FundingOptionsText 拒文 → 「 解法：…」段（含前导空格方便尾部追加）；""=无可推导解法。
// 三选项：①补资金（need−avail×R，$50 档向上取整——档位本身即余量）
// ②调杠杆（need×lev/(avail×R)，仅 lev 可解析）③调小名义（avail×R×lev）。
// okx 张数拒文无 $ 额 → ①③ 文字形式；mt5 无 lev → ③ 改述数量调小。
// acct=调用方已解析账户标签（D-563 S5：原 ^\S+? margin 锚会错抓 "execute:"）；
// acct="" → 「该账户」兜底。
func FundingOptionsText(msg, acct string) string {
	if !IsFundingRejectMsg(msg) {
		return ""
	}
	need, nOK := rejectNum(rejectNeedRe, msg)
	avail, aOK := rejectNum(rejectAvailRe, msg)
	ratio, rOK := rejectNum(rejectRatioRe, msg)
	lev, lOK := rejectNum(rejectLevRe, msg)
	account := "该账户"
	if acct != "" {
		account = acct + "账户"
	}
	var opts []string
	switch {
	case nOK && aOK && rOK:
		if shortfall := need - avail*ratio; shortfall > 0 {
			opts = append(opts, fmt.Sprintf("向%s补可用资金 ≥$%d", account, ceil50(shortfall)))
		}
		if lOK && lev > 0 && avail*ratio > 0 {
			opts = append(opts,
				fmt.Sprintf("或把该账户杠杆 %.0f× 调至 ≥%.0f×（以该合约支持为准）", lev, math.Ceil(need*lev/(avail*ratio))),
				fmt.Sprintf("或单笔开仓金额降至 ≤$%.0f", math.Round(avail*ratio*lev)))
		} else {
			opts = append(opts, "或单笔开仓数量调小")
		}
	case strings.Contains(msg, "张"): // okx 张数拒文（sz/avail 张无 $ 额）→ ①③ 文字形式
		opts = append(opts, "向该账户补可用资金", "或单笔开仓数量调小")
	}
	if len(opts) == 0 {
		return ""
	}
	return " 解法：" + strings.Join(opts, "；")
}
