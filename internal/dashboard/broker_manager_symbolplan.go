package dashboard

import (
	"context"
	"fmt"
	"math"
	"strings"

	"arb/internal/store/pgstore"
)

// symbolPlan 决定订阅符号与映射（D-042 用户策展模型，canonical 数量用户自定）：
//  1. DB 已有该 venue 人工映射 → 校验 brokerSymbol 存在于服务器，失效项删除，返回。
//  2. 无映射 → 自动映射：既有 canonical（跨全部 venue 已映射）∪ 默认种子，名字+合约匹配者
//     落库并订阅（默认种子 = 首 broker 开箱 bootstrap；合约未知任一侧不阻塞）。
//  3. 全部不中 → identityMap 保底（原样订阅默认符号，个别不存在由订阅容错）。
func (m *BrokerManager) symbolPlan(ctx context.Context, venue string, fetchGroups func(context.Context) ([]SymbolGroup, error)) (map[string]string, []string, error) {
	groups, err := fetchGroups(ctx)
	if err != nil {
		return nil, nil, err
	}
	mappings, err := m.store.ListSymbolMappings(ctx, venue)
	if err != nil {
		return nil, nil, err
	}
	if len(mappings) > 0 {
		// 校验映射有效性：brokerSymbol 必须存在于服务器符号表，否则失效（如旧版 identity 默认表
		// 未匹配 cent 后缀）。失效项删除，全部失效则落到自动映射。
		set := make(map[string]bool, len(groups))
		for _, g := range groups {
			set[g.Symbol] = true
		}
		sm := make(map[string]string)
		keys := make([]string, 0, len(mappings))
		for _, mp := range mappings {
			if !set[mp.BrokerSymbol] {
				_ = m.store.DeleteSymbolMapping(ctx, mp.Venue, mp.BrokerSymbol)
				continue
			}
			sm[mp.BrokerSymbol] = mp.Canonical
			keys = append(keys, mp.BrokerSymbol)
		}
		if len(sm) > 0 {
			return sm, keys, nil
		}
	}
	all, err := m.store.ListSymbolMappings(ctx, "")
	if err != nil {
		return nil, nil, err
	}
	idx := buildCanonicalIdx(all, defaultSymbols)
	sm := make(map[string]string)
	var syms []string
	for _, sug := range classifySymbols(groups, idx) {
		if sug.Status != StatusAuto {
			continue
		}
		sm[sug.BrokerSymbol] = sug.Canonical
		syms = append(syms, sug.BrokerSymbol)
		var cs *float64
		if sug.ContractSize > 0 {
			v := sug.ContractSize
			cs = &v
		}
		_ = m.store.UpsertSymbolMapping(ctx, &pgstore.SymbolMapping{
			Venue: venue, BrokerSymbol: sug.BrokerSymbol, Canonical: sug.Canonical, ContractSize: cs,
		})
	}
	if len(sm) > 0 {
		return sm, syms, nil
	}
	sm, syms = identityMap(defaultSymbols)
	return sm, syms, nil
}

// mappingStatus 建议映射分类（D-042 自动映射 + 建议共用判据）。
type mappingStatus string

const (
	StatusAuto     mappingStatus = "auto"     // 名字+合约匹配（或任一侧未知无法证伪）→ 自动落库 / 一键采纳
	StatusConflict mappingStatus = "conflict" // 名字匹配但合约不符——真实错配（ETF/股票撞原油）→ 待人工确认
)

// Suggestion 名字匹配既有 canonical 的候选映射。
type Suggestion struct {
	BrokerSymbol string
	Group        string
	ContractSize float64
	Canonical    string
	Status       mappingStatus
	Reason       string
}

// canonicalIdx canonical 索引：别名（canonical 名 ∪ 已映射 brokerSymbol）→ canonical；
// 期望合约 = 该 canonical 既有映射合约共识（无已知 = 0，无法校验）。
type canonicalIdx struct {
	aliases  map[string]string
	contract map[string]float64
}

// buildCanonicalIdx 从全部映射 + 默认种子 + 生产 alias 表构建索引（D-066 整合）。
// 种子 canonical 自身名即别名（首 broker bootstrap）；canonicalAliases 表全覆盖（D-066）。
// 同一别名映射到不同 canonical 的异常数据：首见优先（校验后正常数据不出现）。
func buildCanonicalIdx(all []pgstore.SymbolMapping, seeds []string) canonicalIdx {
	idx := canonicalIdx{
		aliases:  make(map[string]string),
		contract: make(map[string]float64),
	}
	// D-066：生产 alias 表先注入（canonical → aliases 全覆盖）
	for canon, aliases := range canonicalAliases {
		c := Canonicalize(canon)
		for _, al := range aliases {
			if _, dup := idx.aliases[al]; !dup {
				idx.aliases[al] = c
			}
		}
	}
	for _, c := range seeds {
		if _, dup := idx.aliases[c]; !dup {
			idx.aliases[c] = Canonicalize(c)
		}
	}
	counts := make(map[string]map[float64]int)
	for _, m := range all {
		if _, dup := idx.aliases[m.BrokerSymbol]; !dup {
			idx.aliases[m.BrokerSymbol] = m.Canonical
		}
		if m.ContractSize != nil {
			if counts[m.Canonical] == nil {
				counts[m.Canonical] = make(map[float64]int)
			}
			counts[m.Canonical][*m.ContractSize]++
		}
	}
	for c, m := range counts {
		best, n := 0.0, 0
		for v, k := range m {
			if k > n {
				best, n = v, k
			}
		}
		idx.contract[c] = best
	}
	return idx
}

// classifySymbols 把服务器符号对照 canonical 索引分类。名字匹配（精确优先，否则 cent 后缀
// EURUSDm→EURUSD）；合约双方已知且不符 = conflict（真错配），其余（匹配或任一侧未知）= auto。
func classifySymbols(groups []SymbolGroup, idx canonicalIdx) []Suggestion {
	var out []Suggestion
	for _, g := range groups {
		canon, ok := idx.aliases[g.Symbol]
		if !ok {
			if c, found := idx.aliases[trimCentSuffix(g.Symbol)]; found {
				canon, ok = c, true
			}
		}
		if !ok {
			continue
		}
		sug := Suggestion{
			BrokerSymbol: g.Symbol, Group: g.Group, ContractSize: g.ContractSize,
			Canonical: canon, Status: StatusAuto,
		}
		if exp := idx.contract[canon]; exp > 0 && g.ContractSize > 0 && !strings.HasSuffix(g.Symbol, "m") {
			ratio := math.Max(exp/g.ContractSize, g.ContractSize/exp)
			if ratio > 2.0 {
				sug.Status = StatusConflict
				sug.Reason = fmt.Sprintf("合约规格 %v 与 %s 期望 %v 比值 %.1f 超阈值 2.0", g.ContractSize, canon, exp, ratio)
			}
		}
		out = append(out, sug)
	}
	return out
}

// trimCentSuffix 去 MT5 cent 账号符号后缀（EURUSDm → EURUSD）+ Pepperstone spot 指数 '#' 前缀（#UK100 → UK100）。
// 只处理安全后缀/前缀（不改变品种身份）；'365'/'EURO' 等改变品种身份的不处理（合约门禁拦截）。
func trimCentSuffix(s string) string {
	if strings.HasSuffix(s, "m") {
		s = s[:len(s)-1]
	}
	if strings.HasPrefix(s, "#") {
		s = s[1:]
	}
	return s
}

// identityMap 保底 identity 映射（服务器符号解析失败时用，原样订阅默认符号）。
func identityMap(defaults []string) (map[string]string, []string) {
	sm := make(map[string]string, len(defaults))
	for _, s := range defaults {
		sm[s] = s
	}
	return sm, defaults
}

// defaultSymbols 无映射时默认订阅（与 cmd/core brokerSymbols 默认一致）。
var defaultSymbols = []string{"EURUSD", "GBPUSD", "XAUUSD", "BTCUSD", "ETHUSD"}

// topGroup 取 SymGroup.GroupName 反斜杠路径的顶层段（Forex\Major\EURUSD → Forex）。
// 无分隔符原样返回。MT5 服务器品种分组数据源（归档 symbol_map_handlers.go topGroup 同法）。
// accountTypeGroupPrefix：部分 broker 把账号类型作为分组路径第一段（Exness 实测
// `Standard\Forex\EURUSDm` 全 356 符号第一段均 Standard），市场分组在第二段；
// 这些 token 一律跳过取下一段，否则分组被账号类型拍平（浏览器只剩一个"Standard"）。
var accountTypeGroupPrefix = map[string]bool{
	"standard": true, "cent": true, "micro": true, "demo": true, "real": true,
	"ecn": true, "fixed": true, "pro": true, "netting": true, "hedging": true, "instant": true,
}

// topGroup 取分组路径的"市场分组"段：跳过账号类型前缀（Standard/Cent/Micro…），
// 返回第一个非账号类型段；无分隔/全为账号类型 → 原样返回。
func topGroup(name string) string {
	parts := strings.Split(name, "\\")
	for _, p := range parts {
		if p == "" || accountTypeGroupPrefix[strings.ToLower(p)] {
			continue
		}
		return p
	}
	return name
}
