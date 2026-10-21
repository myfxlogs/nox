// match.go —— D-187：模板×账本纯匹配核（零 IO 可测）。
// 条件求值口径：credential=凭据有无 / capital=资金桶≥min_usd / holding=持仓≥min_qty
// / permission=能力标签有无（账本暂无权限面→默认缺→缺口请求，禁静默）
// / constraint=约束行存在（subject 命中即视为已喂；限值比较留 D-188 精细化）。
package oppmatch

import "sort"

// LedgerView 账本求值投影（由 pgstore.CapabilityLedger 折算；stable 资金并口）。
type LedgerView struct {
	Credentials map[string]bool          // venue、"venue:type"、"type:<venue_type>" 三键
	CapitalUSD  float64                  // 稳定币桶合计（USD/USDT/USDC 归并）
	CapitalBy   map[string]float64       // 非稳币桶按 ccy 直挂
	Holdings    map[string]float64       // asset → qty
	Constraints map[string]ConstraintVal // subject → 已喂约束（qualifier+value）
	Permissions map[string]bool          // endpoint 权限标签（present 探测面）
	PermStates  map[string]string        // D-193②：permission 三态原样（present|absent|unknown——收口哨用）
}

// ConstraintVal 约束限值（qualifier=max/min 时 Value 参与数值比较）。
type ConstraintVal struct {
	Qualifier string
	Value     *float64
}

// Cond 模板条件行（与 fin_opp_template_cond 同构）。
type Cond struct {
	Kind      string
	Subject   string
	Qualifier string
	MinVal    *float64
}

// Template 模板投影（id+轨+条件+报价）。
type Template struct {
	ID        string
	ExecTrack string
	Conds     []Cond
	APR       *float64 // 报价源最佳 APR（nil=报价缺）
}

// Result 单模板裁决：missing 空=可提案，非空=缺口集合。
type Result struct {
	TemplateID string
	ExecTrack  string
	APR        *float64
	Venue      string // 提案 venue（eval 深度源键——run.go 填值）
	// GrossEvidence gross 项证源覆盖（idle 产案等非报价径；空=默认 quoteFor 口径）。
	GrossEvidence string
	Missing       []Cond
}

var stableCcy = map[string]bool{"USD": true, "USDT": true, "USDC": true}

// EvalCond 单条件求值（不满足=缺口，种类直译请求文案由 runner 生成）。
func EvalCond(l LedgerView, c Cond) bool {
	switch c.Kind {
	case "credential":
		// 三级命中：venue → venue:type → type:<venue_type>（D-188①——
		// subject="mt5"/"option"/"futures" 是 venue_type 级凭据，任一所行命中即算在）。
		return l.Credentials[c.Subject] || l.Credentials["type:"+c.Subject]
	case "capital":
		if stableCcy[c.Subject] {
			return c.MinVal != nil && l.CapitalUSD >= *c.MinVal
		}
		return c.MinVal != nil && l.CapitalBy[c.Subject] >= *c.MinVal
	case "holding":
		return c.MinVal != nil && l.Holdings[c.Subject] >= *c.MinVal
	case "permission":
		return l.Permissions[c.Subject]
	case "constraint":
		// subject 存在即已喂；cond 带 MinVal 且约束有数值限→数值比较
		// （qualifier=max→约束值≤cond 界、min→约束值≥cond 界）。
		cv, ok := l.Constraints[c.Subject]
		if !ok {
			return false
		}
		if c.MinVal == nil || cv.Value == nil {
			return true
		}
		switch cv.Qualifier {
		case "max":
			return *cv.Value <= *c.MinVal
		case "min":
			return *cv.Value >= *c.MinVal
		}
		return true
	}
	return false // 未知 kind=不满足 fail-closed
}

// gapCapitalTarget D-652-C：capital 缺口的核验锚投影（裸币种 target+MinVal 额——
// target_bucket=subject 币种、target_usd=cond 下限；非 capital 零值不填）。
func gapCapitalTarget(c Cond) (string, *float64) {
	if c.Kind != "capital" {
		return "", nil
	}
	return c.Subject, c.MinVal
}

// EvalCondTri D-193② 三态求值（缺口自动收口哨用）："ok"|"miss"|"unknown"——
// permission kind 行缺（探针未就绪）或 state=unknown（传输层败）→unknown 不收不复开；
// 其余 kind=账本断言面确定性二态。
func EvalCondTri(l LedgerView, c Cond) string {
	if c.Kind == "permission" {
		switch l.PermStates[c.Subject] {
		case "present":
			return "ok"
		case "absent":
			return "miss"
		default: // 行缺/unknown=探针面未就绪——判不了
			return "unknown"
		}
	}
	if EvalCond(l, c) {
		return "ok"
	}
	return "miss"
}

// Match 全模板裁决：可提案者按净 APR 降序（nil 报价殿后）；
// 缺条件者不静默——missing 全量回传由 runner 落 fin_condition_gap。
func Match(ts []Template, l LedgerView) []Result {
	out := make([]Result, 0, len(ts))
	for _, t := range ts {
		r := Result{TemplateID: t.ID, ExecTrack: t.ExecTrack, APR: t.APR}
		for _, c := range t.Conds {
			if !EvalCond(l, c) {
				r.Missing = append(r.Missing, c)
			}
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		// 提案组在前（missing 空），组内 APR 降序、nil 殿后。
		mi, mj := len(out[i].Missing) == 0, len(out[j].Missing) == 0
		if mi != mj {
			return mi
		}
		if out[i].APR == nil || out[j].APR == nil {
			return out[i].APR != nil
		}
		return *out[i].APR > *out[j].APR
	})
	return out
}
