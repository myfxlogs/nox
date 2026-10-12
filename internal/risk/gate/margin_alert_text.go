// margin_alert_text.go —— D-556：资金拒单飞书白话化（纯函数，禁 IO）。
// 病灶：业主收到的资金拒单是 executor 黑话原文（margin precheck/leg/need/avail），
// 不知道「给谁注资、差多少、怎么做」。本件把已识别拒文翻成注资指引白话；
// 识别失败走泛化文案（原始明细附尾，不猜不丢）。账户名推导复用 D-487 既有约定
// （venueTypeCN 词表与前端 PositionsPageHelpers VENUE_TYPE_CN 同词；venue_type 是
// DB CHECK 闭集——0142 宪法——新类型进不了库，无静默漏译漂移）；MT 别名走
// ListAccountAliases 既有数据源（main 装配注入，禁造新映射表=业主令不硬编码）。
// KB error-sentinel-unreachable-past-string-boundary：intent.Execute 字符串化后
// err 对象已死——msg 子串解析是该边界的唯一可行路径（B-005 同裁定）。
package gate

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"arb/internal/data/keydomain"
	"arb/internal/platform/errclass"
)

// venueTypeCN 账户类型中文名（与前端 VENUE_TYPE_CN 同词表；DB CHECK 闭集防漂移）。
var venueTypeCN = map[string]string{"perp": "永续", "spot": "现货", "funding": "资金费率"}

// accountLabel 账户名推导：type∈{mt4,mt5}→「MT5 账户 <alias，空回落 bare 账号>」；
// type 有中文名→「<bare>·<词>账户」；type 空但键带冒号（同所拒文腿型不可区分，
// alertLeg "bare:" 形态）→「<bare> 交易账户」不猜腿型；未知 type 原样透出 vk 不猜。
// venue 品牌名=bare 原样（gate/okx/binance），零品牌映射表。
func accountLabel(vk string, aliasOf func(string) string) string {
	i := strings.LastIndexByte(vk, ':')
	if i < 0 {
		return vk // 裸键（无 type 段）原样
	}
	bare, typ := vk[:i], vk[i+1:]
	if typ == "" {
		return bare + " 交易账户"
	}
	if typ == "mt4" || typ == "mt5" {
		alias := ""
		if aliasOf != nil {
			alias = aliasOf(vk)
		}
		if alias != "" {
			return "MT5 账户 " + alias
		}
		return "MT5 账户 " + bare
	}
	if cn, ok := venueTypeCN[typ]; ok {
		return bare + "·" + cn + "账户"
	}
	return vk // 未知 type 不猜
}

// alertLeg 从拒文提腿 venue 键：优先 `leg <venueKey>:`（pipeline_execute 包裹位）；
// 失败查 intent 双腿原文；再败按裸 venue 名匹配（executor 拒文前缀=裸名，如
// "gate margin insufficient"/"okx margin insufficient"——裸名命中即全键归因）；
// 再败=空（venue 未知→泛化文案）。
func alertLeg(msg, venueBuy, venueSell string) string {
	if m := regexp.MustCompile(`leg ([^ :]+):`).FindStringSubmatch(msg); m != nil {
		return m[1]
	}
	if venueBuy != "" && strings.Contains(msg, venueBuy) {
		return venueBuy
	}
	if venueSell != "" && strings.Contains(msg, venueSell) {
		return venueSell
	}
	// 裸 venue 名前缀归因（executor 拒文前缀=裸名，如 "gate margin insufficient"）；
	// 双腿同前缀（同所 binance spot×perp）不可区分——返回带 "same" 标记的裸名，
	// 上层按「账户不明」白话（交易所名仍可透出，禁猜腿型）。
	bare := keydomain.BareOf(venueBuy)
	buyHit := bare != "" && strings.HasPrefix(strings.ToLower(msg), strings.ToLower(bare))
	sellBare := keydomain.BareOf(venueSell)
	sellHit := sellBare != "" && strings.HasPrefix(strings.ToLower(msg), strings.ToLower(sellBare))
	switch {
	case buyHit && sellHit:
		return bare + ":" // 同所不可区分：裸名键（accountLabel 未知 type 原样=交易所名）
	case buyHit:
		return venueBuy
	case sellHit:
		return venueSell
	}
	// MT 拒文前缀=腿类型名（"mt5 margin insufficient"），无账号号——按 intent 双腿
	// type 匹配归因（mt×cex 组合 MT 腿唯一，可安全归因）。
	if strings.HasPrefix(low2(msg), "mt5") || strings.HasPrefix(low2(msg), "mt4") {
		want := strings.SplitN(low2(msg), " ", 2)[0]
		for _, vk := range []string{venueBuy, venueSell} {
			if vk != "" && keydomain.Parse(vk).Type == want {
				return vk
			}
		}
	}
	return ""
}

// low2 小写化（局部便捷）。
func low2(s string) string { return strings.ToLower(s) }

var (
	reNeedAvailReserve = regexp.MustCompile(`margin insufficient: need ([0-9.]+) > avail ([0-9.]+) × ([0-9.]+)`)
	reOkxSz            = regexp.MustCompile(`margin insufficient: sz ([0-9.]+) 张 > avail ([0-9.]+) 张`)
	reSpotCash         = regexp.MustCompile(`spot cash insufficient: need ([0-9.]+) (\S+) > avail ([0-9.]+)`)
	reMtRequired       = regexp.MustCompile(`margin insufficient(?: \(estimate\))?: required ([0-9.]+) > avail ([0-9.]+)`)
	reLiqEst           = regexp.MustCompile(`liq-distance est=([0-9.]+) < floor=([0-9.]+)`)
	reLiqCross         = regexp.MustCompile(`liq-distance cross notional/equity=([0-9.]+) > cap=([0-9.]+)`)
)

// f2 格式化金额（去浮点尾噪；≥100 取整，<100 两位小数）。
func f2(v float64) string {
	if v >= 100 {
		return strconv.FormatFloat(math.Round(v), 'f', 0, 64)
	}
	return strconv.FormatFloat(v, 'f', 2, 64)
}

// fundingAlertBody 拒文→白话正文（已识别=注资指引；未识别资金拒=泛化；非资金拒=""）。
func fundingAlertBody(msg, legVK string, aliasOf func(string) string) string {
	acct := ""
	if legVK != "" {
		acct = accountLabel(legVK, aliasOf)
	}
	low := strings.ToLower(msg)
	if m := reNeedAvailReserve.FindStringSubmatch(msg); m != nil {
		need, _ := strconv.ParseFloat(m[1], 64)
		avail, _ := strconv.ParseFloat(m[2], 64)
		reserve, _ := strconv.ParseFloat(m[3], 64)
		cap := avail * reserve
		gap := math.Max(0, need-cap)
		name := acct
		if name == "" {
			name = legVK
		}
		return fmt.Sprintf("%s可用$%s，本次开仓需保证金$%s，规则另留备用金→可动用上限$%s，还差约$%s。请向%s转入USDT（站内划转即可），到账后系统自动重试。",
			name, f2(avail), f2(need), f2(cap), f2(gap), name)
	}
	if m := reOkxSz.FindStringSubmatch(msg); m != nil {
		return fmt.Sprintf("OKX 交易账户保证金不足：本次需 %s 张合约、当前只能开 %s 张。请向 OKX 交易账户补充 USDT，或等系统减小仓位后自动重试。", m[1], m[2])
	}
	if m := reSpotCash.FindStringSubmatch(msg); m != nil {
		need, ccy, avail := m[1], m[2], m[3]
		name := acct
		// spot cash 拒文自证 spot 腿（买币钱）——同所不可区分键时按 spot 归名（拒文证据，非猜）。
		if strings.HasSuffix(legVK, ":") {
			name = strings.TrimSuffix(legVK, ":") + "·" + venueTypeCN["spot"] + "账户"
		}
		if name == "" {
			name = legVK
		}
		return fmt.Sprintf("%s里买币的钱不够：需要 %s %s、可用 %s。请向%s充值 %s，到账后自动重试。", name, need, ccy, avail, name, ccy)
	}
	if m := reMtRequired.FindStringSubmatch(msg); m != nil {
		// 账户名走 accountLabel（别名命中显别名，空回落 bare 账号——统一推导零旁路）。
		name := acct
		if name == "" {
			name = "MT5 账户 " + legVK
		}
		return fmt.Sprintf("%s可用保证金不足：本次需 %s、可用 %s（已含安全垫折扣）。请向该 MT5 账户入金，到账后自动重试。", name, m[1], m[2])
	}
	if m := reLiqEst.FindStringSubmatch(msg); m != nil {
		name := acct
		if name == "" {
			name = legVK
		}
		return fmt.Sprintf("按当前杠杆开仓后离爆仓线太近（约 %s%%，要求≥%s%%）被拦下。向%s补充保证金可拉开安全距离，或等系统降低仓位后自动重试。", m[1], m[2], name)
	}
	if m := reLiqCross.FindStringSubmatch(msg); m != nil {
		name := acct
		if name == "" {
			name = legVK
		}
		return fmt.Sprintf("本次开仓名义金额是账户权益的 %s 倍（上限 %s 倍）被拦下。向%s补充权益资金，或等系统减小仓位后自动重试。", m[1], m[2], name)
	}
	// 泛化资金拒（识别失败不丢）：账户名或该腿 venue+原始明细。
	if strings.Contains(low, "margin insufficient") || strings.Contains(low, "margin is insufficient") {
		who := acct
		if who == "" {
			who = legVK
		}
		if who == "" {
			return "交易账户保证金不足，交易所拒单。请检查相关账户余额并补充资金；明细见 autotrade_log。"
		}
		return fmt.Sprintf("%s保证金不足，交易所拒单。请检查该账户余额并补充资金；明细见 autotrade_log。", who)
	}
	return "" // 非资金拒文（调用方零告警路径）
}

// fundingAlertText 消息骨架：【请加保证金】头+白话正文+节流说明+技术明细附尾。
// 非资金拒文 → 空串（调用方不产告警）。
func fundingAlertText(intent TradeIntent, msg string, aliasOf func(string) string) string {
	body := fundingAlertBody(msg, alertLeg(msg, intent.VenueBuy, intent.VenueSell), aliasOf)
	if body == "" {
		return ""
	}
	return fmt.Sprintf("【请加保证金】%s 套利开仓被拦下（%s）\n%s\n※ 同一组合+账户 6 小时内只提醒一次；技术明细：%s",
		intent.Canonical, intent.Strategy, body, errclass.TruncateNote(msg, 120))
}
