package dashboard

import (
	"context"
	"fmt"
	"log"
	"time"
)

// ReloadSymbolMap 重读 venue 映射并推送到运行中适配器（保存/删除映射后运行时生效）。
// D-383 A2.2/A2.3：同步刷新 canonical 发射抑制集 + 歧义组告警（(venue,canonical) 键冷却）。
func (m *BrokerManager) ReloadSymbolMap(ctx context.Context, venue string) {
	m.mu.Lock()
	r := m.running[venue]
	m.mu.Unlock()
	if r == nil || r.setSymMap == nil {
		return
	}
	mappings, err := m.store.ListSymbolMappings(ctx, venue)
	if err != nil {
		log.Printf("[broker] %s list mappings: %v", venue, err)
		return
	}
	_, blocked, dead := primarySplit(mappings)
	m.alertAmbiguousGroups(dead)
	// D-594：单位敏感集重建 + 未标定敏感行并入 blocked（与 symbolPlan 同规则）。
	sensitive := m.unitSensitiveSet(ctx, venue)
	scales := unitScalePlan(mappings, sensitive, blocked)
	sm := make(map[string]string, len(mappings))
	for _, mp := range mappings {
		sm[mp.BrokerSymbol] = mp.Canonical
	}
	r.setSymMap(sm)
	if r.setEmitBlocked != nil {
		r.setEmitBlocked(blocked)
	}
	if r.setUnitScales != nil {
		r.setUnitScales(scales, sensitive)
	}
	log.Printf("[broker] %s symbol map refreshed (%d)", venue, len(sm))
	m.kickSwap()     // 映射增删后立即补一轮 swap（D-060 F2，新符号缺价自然走冷启动退避自愈）
	m.kickSessions() // D-029 R2：映射增删后立即补一轮周表
}

// BrokerSymbols 从运行中适配器拉取服务器符号表（附 broker 分组，辅助补映射搜索）。
// MT5 走 symbolGroups（SymbolParamsMany 取 GroupName 顶层段）；MT4/降级回退 symbols（group 空）。
func (m *BrokerManager) BrokerSymbols(ctx context.Context, venue string) ([]SymbolGroup, error) {
	m.mu.Lock()
	r := m.running[venue]
	m.mu.Unlock()
	if r == nil || r.symbols == nil {
		return nil, fmt.Errorf("venue %s 未连接", venue)
	}
	if r.symbolGroups != nil {
		return m.cachedSymbolGroups(ctx, venue, r)
	}
	names, err := r.symbols(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]SymbolGroup, 0, len(names))
	for _, s := range names {
		out = append(out, SymbolGroup{Symbol: s})
	}
	return out, nil
}

// Resubscribe 运行时补订阅该 venue 的新符号（保存映射后调用）。
// D-047 缺陷③：映射了不重订阅 = 没报价流；mtapi 会话订阅幂等，已开报价流自动接上新符号。
// D-383 A6：入参过同规则滤——非合格行（多行组非 primary）不进订阅；映射查询失败 fail-closed 不订。
func (m *BrokerManager) Resubscribe(venue string, symbols []string) {
	m.mu.Lock()
	r := m.running[venue]
	m.mu.Unlock()
	if r == nil || r.resubscribe == nil || len(symbols) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mappings, err := m.store.ListSymbolMappings(ctx, venue)
	if err != nil {
		log.Printf("[broker] %s resubscribe: list mappings: %v（fail-closed 不订）", venue, err)
		return
	}
	ok, _, _ := primarySplit(mappings)
	eligible := make(map[string]bool, len(ok))
	for _, mp := range ok {
		eligible[mp.BrokerSymbol] = true
	}
	filtered := make([]string, 0, len(symbols))
	for _, s := range symbols {
		if eligible[s] {
			filtered = append(filtered, s)
		}
	}
	if len(filtered) == 0 {
		return
	}
	r.resubscribe(filtered)
}

// AllSymbolGroups 返回全部 connected 账号的 symbolGroups（D-045 §3.2 装配用）。
// venue → SymbolGroup 列表；拉取失败的 venue 不出现在结果中（调用方通过 accounts 差集识别）。
func (m *BrokerManager) AllSymbolGroups(ctx context.Context) (map[string][]SymbolGroup, error) {
	m.mu.Lock()
	venues := make([]string, 0, len(m.running))
	for v, r := range m.running {
		if r.symbolGroups != nil {
			venues = append(venues, v)
		}
	}
	runningCopy := make(map[string]*running, len(m.running))
	for k, v := range m.running {
		runningCopy[k] = v
	}
	m.mu.Unlock()

	out := make(map[string][]SymbolGroup, len(venues))
	for _, v := range venues {
		r := runningCopy[v]
		groups, err := m.cachedSymbolGroups(ctx, v, r)
		if err != nil {
			log.Printf("[broker] %s symbolGroups for universe: %v", v, err)
			continue
		}
		out[v] = groups
	}
	return out, nil
}

// BrokerContractSize 返回该 venue 服务器符号的权威合约规格（D-042 零信任：校验数据只信 broker，
// 不信任前端传值）。MT4 / 未运行 / 查询失败 → known=false（合约未知，调用方跳过规格校验）。
// BrokerMinLots 单符号最小手数（D-061 量规格预检）。known=false = 未知（MT4/未运行），调用方放行。
func (m *BrokerManager) BrokerMinLots(ctx context.Context, venue, symbol string) (float64, bool, error) {
	m.mu.Lock()
	r := m.running[venue]
	m.mu.Unlock()
	if r == nil || r.volumeSpec == nil {
		return 0, false, nil
	}
	return r.volumeSpec(ctx, symbol)
}

// BrokerVolumeStep 单符号 volume step（D-070 qty 精度取整）。known=false = 未知，调用方用默认。
func (m *BrokerManager) BrokerVolumeStep(ctx context.Context, venue, symbol string) (float64, bool, error) {
	m.mu.Lock()
	r := m.running[venue]
	m.mu.Unlock()
	if r == nil || r.volumeStep == nil {
		return 0, false, nil
	}
	return r.volumeStep(ctx, symbol)
}

func (m *BrokerManager) BrokerContractSize(ctx context.Context, venue, symbol string) (float64, bool, error) {
	m.mu.Lock()
	r := m.running[venue]
	m.mu.Unlock()
	if r == nil || r.symbolParams == nil {
		return 0, false, nil
	}
	return r.symbolParams(ctx, symbol)
}

// BrokerSymbolMarginSpec D-036：venue 原生符号保证金规格（SymGroup.InitialMargin per-lot
// 定额 + SymbolInfo.ContractSize）——仓位级 margin 估算输入。MT4/未运行/查询失败 →
// ok=false（调用方诚实空态）。
func (m *BrokerManager) BrokerSymbolMarginSpec(ctx context.Context, venue, symbol string) (initialMargin, contractSize float64, ok bool) {
	m.mu.Lock()
	r := m.running[venue]
	m.mu.Unlock()
	if r == nil || r.marginSpec == nil {
		return 0, 0, false
	}
	im, cs, err := r.marginSpec(ctx, symbol)
	if err != nil {
		log.Printf("[broker] margin spec %s %s: %v", venue, symbol, err)
		return 0, 0, false
	}
	return im, cs, true
}

// BrokerRequiredMargin D-068：venue 原生仓位保证金（RequiredMargin RPC，price=0=
// 网关侧现价、type=仓向）。未运行/未接线/查询失败/结果≤0 → ok=false（调用方降级
// D-036 规格估算）。
func (m *BrokerManager) BrokerRequiredMargin(ctx context.Context, venue, symbol string, lots float64, side string) (float64, bool) {
	m.mu.Lock()
	r := m.running[venue]
	m.mu.Unlock()
	if r == nil || r.requiredMargin == nil {
		return 0, false
	}
	v, err := r.requiredMargin(ctx, symbol, lots, side)
	if err != nil {
		log.Printf("[broker] required margin %s %s: %v", venue, symbol, err)
		return 0, false
	}
	return v, true
}

// backfillContracts 补写 venue 既有映射的 NULL 合约（数据源 = broker 权威分组信息）。
// D-042 前写入的历史映射 contract_size=NULL → 规格画像为空，合约校验形同虚设；
// 启动时回填恢复画像基线。未命中符号（如 MT4/停用）保持 NULL = 合约未知，合法跳过校验。
func (m *BrokerManager) backfillContracts(ctx context.Context, venue string, groups []SymbolGroup) {
	bySym := make(map[string]float64, len(groups))
	for _, g := range groups {
		if g.ContractSize > 0 {
			bySym[g.Symbol] = g.ContractSize
		}
	}
	if len(bySym) == 0 {
		return
	}
	if err := m.store.BackfillSymbolContracts(ctx, venue, bySym); err != nil {
		log.Printf("[broker] %s backfill contracts: %v", venue, err)
		return
	}
	log.Printf("[broker] %s contracts backfilled (%d symbols)", venue, len(bySym))
}

// BrokerSymbolMap venue broker 原生符号 → canonical（store broker_symbol_map）。D-051 WP3。
// 查询错误/无映射 → 空 map（诚实，调用方留 canonical=""）。
func (m *BrokerManager) BrokerSymbolMap(ctx context.Context, venue string) map[string]string {
	if m.store == nil {
		return map[string]string{}
	}
	mappings, err := m.store.ListSymbolMappings(ctx, venue)
	if err != nil {
		log.Printf("[broker] %s list symbol mappings: %v", venue, err)
		return map[string]string{}
	}
	sm := make(map[string]string, len(mappings))
	for _, mp := range mappings {
		sm[mp.BrokerSymbol] = mp.Canonical
	}
	return sm
}

// BrokerSymbolMapWithCS 一次查询返回 canonical + contractSize 两个 map（D-124 方案 C 保证金分摊用）。
// csMap 值 0 = 未知（MT4 / 历史映射 nil）；canonical 空字符串 = 未映射。
func (m *BrokerManager) BrokerSymbolMapWithCS(ctx context.Context, venue string) (canonicalMap map[string]string, csMap map[string]float64) {
	canonicalMap = map[string]string{}
	csMap = map[string]float64{}
	if m.store == nil {
		return
	}
	mappings, err := m.store.ListSymbolMappings(ctx, venue)
	if err != nil {
		log.Printf("[broker] %s list symbol mappings (with cs): %v", venue, err)
		return
	}
	for _, mp := range mappings {
		canonicalMap[mp.BrokerSymbol] = mp.Canonical
		if mp.ContractSize != nil && *mp.ContractSize > 0 {
			csMap[mp.BrokerSymbol] = *mp.ContractSize
		}
	}
	return
}

// cachedSymbolGroups 带 per-venue TTL 缓存的 symbolGroups（D-045 §3.2.1）。
// 缓存命中直接返回；过期回退直连，成功更新缓存，失败不下发脏数据（返回 error）。
func (m *BrokerManager) cachedSymbolGroups(ctx context.Context, venue string, r *running) ([]SymbolGroup, error) {
	ttl := m.symbolGroupsTTL
	if ttl <= 0 {
		return r.symbolGroups(ctx)
	}
	r.sgMu.Lock()
	if len(r.sgCache) > 0 && time.Since(r.sgCacheAt) < ttl {
		out := r.sgCache
		r.sgMu.Unlock()
		return out, nil
	}
	r.sgMu.Unlock()

	groups, err := r.symbolGroups(ctx)
	if err != nil {
		return nil, err
	}
	r.sgMu.Lock()
	r.sgCache = groups
	r.sgCacheAt = time.Now()
	r.sgMu.Unlock()
	return groups, nil
}
