package dashboard

import (
	"context"
	"fmt"
	"math"
	"strings"

	"nox/internal/data/adapter"
	"nox/internal/store/pgstore"
)

// primarySplit D-383 A2.2/A2.3：按「is_primary 或 (venue,canonical) 组内恰一行（隐式 primary）」
// 切分映射行。canonical==” 行不属撞车空间（migration 0122 索引同域排除）恒合格。
// 返回：合格行集（可订阅/canonical 供数）、canonical 发射须抑制的 broker_symbol 集
// （多行组非 primary 行——native/variant fact 照常，仅 canonical 标签停发）、
// 歧义组键（venue|canonical，多行零 primary——canonical 事实源 fail-dead，须 alerts 表告警）。
func primarySplit(mappings []pgstore.SymbolMapping) (ok []pgstore.SymbolMapping, blockedSyms map[string]bool, deadGroups map[string]bool) {
	count := make(map[string]int)
	primarySeen := make(map[string]bool)
	for _, m := range mappings {
		if m.Canonical == "" {
			continue
		}
		key := m.Venue + "|" + m.Canonical
		count[key]++
		if m.IsPrimary {
			primarySeen[key] = true
		}
	}
	blockedSyms = make(map[string]bool)
	deadGroups = make(map[string]bool)
	for _, m := range mappings {
		key := m.Venue + "|" + m.Canonical
		if m.Canonical == "" || m.IsPrimary || count[key] == 1 {
			ok = append(ok, m)
			continue
		}
		blockedSyms[m.BrokerSymbol] = true
		if !primarySeen[key] {
			deadGroups[key] = true
		}
	}
	return ok, blockedSyms, deadGroups
}

// symbolPlan 决定订阅符号与映射（D-042 用户策展模型，canonical 数量用户自定）：
//  1. DB 已有该 venue 人工映射 → 校验 brokerSymbol 存在于服务器，失效项删除，返回。
//  2. 无映射 → 自动映射：既有 canonical（跨全部 venue 已映射）∪ 默认种子，名字+合约匹配者
//     落库并订阅（默认种子 = 首 broker 开箱 bootstrap；合约未知任一侧不阻塞）。
//  3. 全部不中 → identityMap 保底（原样订阅默认符号，个别不存在由订阅容错）。
//
// D-383 A2.2：返回 (全量 sm 供适配器 native/variant fact、合格行订阅集、canonical 发射抑制集)。
// 合格行=is_primary 或组内恰一行（隐式 primary）；多行零 primary 组不订阅并 alerts 表告警
// （A2.3，fail-dead 宁死勿错配）；primary 禁止默默猜（SSOT A2.6，Upsert 恒默认 false）。
// D-594：另返回 scale 注入面（已标定行）与单位敏感 canonical 集；敏感 canonical 的未标定行
// 并入 blocked（保留订阅——native 帧照发，canonical 槽禁占，发射口 emitPlan 同款抑制）。
func (m *BrokerManager) symbolPlan(ctx context.Context, venue string, fetchGroups func(context.Context) ([]SymbolGroup, error)) (map[string]string, []string, map[string]bool, map[string]adapter.SymbolScale, map[string]bool, error) {
	groups, err := fetchGroups(ctx)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	sensitive := m.unitSensitiveSet(ctx, venue)
	mappings, err := m.store.ListSymbolMappings(ctx, venue)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	if len(mappings) > 0 {
		// 校验映射有效性：brokerSymbol 必须存在于服务器符号表，否则失效（如旧版 identity 默认表
		// 未匹配 cent 后缀）。失效项删除，全部失效则落到自动映射。
		set := make(map[string]bool, len(groups))
		for _, g := range groups {
			set[g.Symbol] = true
		}
		valid := make([]pgstore.SymbolMapping, 0, len(mappings))
		for _, mp := range mappings {
			if !set[mp.BrokerSymbol] {
				_ = m.store.DeleteSymbolMapping(ctx, mp.Venue, mp.BrokerSymbol)
				continue
			}
			valid = append(valid, mp)
		}
		ok, blocked, dead := primarySplit(valid)
		m.alertAmbiguousGroups(dead)
		sm := make(map[string]string, len(valid)) // 全量（含 blocked 行）——适配器 native/variant fact 仍需
		keys := make([]string, 0, len(ok))        // 订阅集=合格行（A6：非 primary 不进订阅，报价槽恒 primary）
		for _, mp := range valid {
			sm[mp.BrokerSymbol] = mp.Canonical
		}
		for _, mp := range ok {
			keys = append(keys, mp.BrokerSymbol)
		}
		if len(sm) > 0 {
			return sm, keys, blocked, unitScalePlan(valid, sensitive, blocked), sensitive, nil
		}
	}
	all, err := m.store.ListSymbolMappings(ctx, "")
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	idx := buildCanonicalIdx(all, defaultSymbols)
	for _, sug := range classifySymbols(groups, idx) {
		if sug.Status != StatusAuto {
			continue
		}
		var cs *float64
		if sug.ContractSize > 0 {
			v := sug.ContractSize
			cs = &v
		}
		_ = m.store.UpsertSymbolMapping(ctx, &pgstore.SymbolMapping{
			Venue: venue, BrokerSymbol: sug.BrokerSymbol, Canonical: sug.Canonical, ContractSize: cs,
		})
	}
	// D-383 A2.2/A8：自动映射行 is_primary 恒 false（Upsert 不默默猜）——重读落库态再切分：
	// 单行组=隐式 primary 可订阅；多行零 primary 组（同 canonical 匹配多符号）不订阅+告警待种子。
	fresh, err := m.store.ListSymbolMappings(ctx, venue)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	ok, blocked, dead := primarySplit(fresh)
	m.alertAmbiguousGroups(dead)
	sm := make(map[string]string, len(fresh))
	syms := make([]string, 0, len(ok))
	for _, mp := range fresh {
		sm[mp.BrokerSymbol] = mp.Canonical
	}
	for _, mp := range ok {
		syms = append(syms, mp.BrokerSymbol)
	}
	if len(sm) > 0 {
		return sm, syms, blocked, unitScalePlan(fresh, sensitive, blocked), sensitive, nil
	}
	sm, syms = identityMap(defaultSymbols)
	return sm, syms, nil, nil, sensitive, nil
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

// trimCentSuffix 去 MT5 cent 账号符号后缀（EURUSDm → EURUSD）+ Pepperstone spot 指数 '#' 前缀（#UK100 → UK100）
// + XM '#' 后缀命名变体（BTCUSD# → BTCUSD / US100Cash# → US100Cash，D-148）。
// 只处理安全后缀/前缀（不改变品种身份）；'365'/'EURO'/'-MMMYY' 到期期货等改变品种身份的不处理（合约门禁拦截）。
func trimCentSuffix(s string) string {
	s = strings.TrimSuffix(s, "#")
	s = strings.TrimSuffix(s, "m")
	s = strings.TrimPrefix(s, "#")
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
