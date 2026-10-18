// Service setter 注入面（从 service.go 拆出控行数 <300）。
// 全部为依赖注入 seam（main 装配 / 测试覆写），无业务逻辑。
package dashboard

import (
	"context"
	"nox/internal/data/collect/cmc"
	"time"

	"nox/internal/data/bus"
	"nox/internal/data/keydomain"
	"nox/internal/research/econ"
	"nox/internal/store/pgstore"
	"nox/internal/trading/exec"
)

// SetBalanceLedger D-542：注入活账本（与 cex 私有流同实例；装配一次，nil 安全）。
func (s *Service) SetBalanceLedger(l *bus.BalanceLedger) {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	s.balLedger = l
	s.capView = nil // D-549：读口重建（账本实例变更）
}

// SetCapitalGapFetcher D-546：注入三缺口实拉器（生产=RPC 内自装配；测试注入计数 fake）。
func (s *Service) SetCapitalGapFetcher(f CapitalGapFetcher) { s.capGaps = f }

// SetVenueAvailFor D-546 R1：注入 venue 钱包树实拉缝（测试 stub；nil=生产 querier 装配）。
func (s *Service) SetVenueAvailFor(f func(ctx context.Context, venue string) (venueAvailFetcher, bool)) {
	s.capAvailFor = f
}

// Store 返回底层 store（D-070 autotrade engine 用）。
func (s *Service) Store() *pgstore.Store { return s.store }

// AutoClosePosition 平仓（D-070 autotrade engine 自动平仓用，区别于 RPC ClosePosition）。
func (s *Service) AutoClosePosition(ctx context.Context, posID int64) error {
	return s.pipeline.ClosePosition(ctx, posID)
}

// ResumeClose D-189 P0-4：重触发 CLOSING 持仓平仓（reconcile 超时自动重试入口）。
// 只转发 pipeline.ResumeClose，不复制执行逻辑。
func (s *Service) ResumeClose(ctx context.Context, posID int64) error {
	return s.pipeline.ResumeClose(ctx, posID)
}

// ClaimClose D-189 R1：转发 store.ClaimClose（OPEN/LEGGED→CLOSING 原子领取）。
// reconcile auto_close step c 先 ClaimClose 再 ResumeClose，修复 position 仍 OPEN 时
// ResumeClose 拒绝非 CLOSING 导致永久卡死。
func (s *Service) ClaimClose(ctx context.Context, posID int64) error {
	return s.store.ClaimClose(ctx, posID)
}

// SetSettler 注入结算器（D-087 §3.2，手动平仓后调）。nil = 未接线（RPC 仍可用，仅不结算）。
func (s *Service) SetSettler(st Settler) { s.settler = st }

// SetProbeReporter 注入探针平仓取证报告触发钩（D-048③；nil=不生成报告）。
// 钩在 finalizeProbeClose 成功后同步调用——注入方内部自开 goroutine 跑 LLM
// （平仓路径不被报告延迟阻塞；测试注同步 fake 断言恰好一次）。
func (s *Service) SetProbeReporter(fn func(ctx context.Context, posID int64)) { s.probeReporter = fn }

// SetTradeModes 注入单腿 trade_mode 查询（D-060 F5）。main 在 factCache 处装配；nil = 预检放行。
func (s *Service) SetTradeModes(f func(canonical, venueKey string) (int64, bool)) {
	s.tradeModes = f
}

// SetLegContractSize 注入 MT 腿合约规格查询（D-060 实施修正②）。nil = 未接线（resolveLegs 报错）。
func (s *Service) SetLegContractSize(f func(venueKey, venueSymbol string) (float64, bool)) {
	s.legContractSize = f
}

// SetLegUnitQty 注入 MT 腿 canonical 单位数/手查询（D-594：contract_size×qty_scale）。
// nil = 未接线（legContractSizeOf 回退原生 CS——兼容旧测试，生产 main 已接线）。
func (s *Service) SetLegUnitQty(f func(venueKey, venueSymbol string) (float64, bool)) {
	s.legUnitQty = f
}

// LegUnitQty D-594：返回 canonical 单位数/手 resolver（autotrade swaparb rate canonical 中价配对用）。
func (s *Service) LegUnitQty() func(venueKey, venueSymbol string) (float64, bool) {
	return s.legUnitQty
}

// SetPrimarySymbol D-383 A2.4：注入执行解析 primary-only 闸。nil = 未接线（resolveLegs 跳过校验）。
func (s *Service) SetPrimarySymbol(f func(venueKey, canonical string) (string, error)) {
	s.primarySymbol = f
}

// SetCexContractSize 注入 CEX perp 腿合约面值查询（D-311 S3，venue=bare+venueSymbol → ct_val）。
// nil = 未接线（legContractSizeOf 对 :perp 腿 fail-closed 报错）。
func (s *Service) SetCexContractSize(f func(venue, venueSymbol string) (float64, bool)) {
	s.cexContractSize = f
}

// LegContractSize D-165 S2：返回 legContractSize resolver（autotrade engine S2 notional 换算用）。
func (s *Service) LegContractSize() func(venueKey, venueSymbol string) (float64, bool) {
	return s.legContractSize
}

// SetMaxLegImbalancePct 注入卖腿名义不平衡上限（百分比；≤0 回退默认 2）。
func (s *Service) SetMaxLegImbalancePct(p float64) { s.maxLegImbalance = p }

// SetLegMinLots 注入 MT 腿最小手数查询（D-061）。nil/未知 = 放行。
func (s *Service) SetLegMinLots(f func(venueKey, venueSymbol string) (float64, bool)) {
	s.legMinLots = f
}

// SetLegVolumeStep 注入 MT 腿 volume step 查询（D-070 qty 精度取整）。nil/未知 = 用默认 0.01。
func (s *Service) SetLegVolumeStep(f func(venueKey, venueSymbol string) (float64, bool)) {
	s.legVolumeStep = f
}

// SetOppContractSize 注入机会卡 USD 派生用合约规格查询（D-062）。nil = 未接线 → USD 字段全 0（前端回退 bps）。
func (s *Service) SetOppContractSize(f func(canonical, venueKey string) (float64, bool)) {
	s.oppContractSize = f
}

// OppContractSize 透传机会卡 CS 查询（D-135 autotrade engine sizing 用）。
// nil 接线 → (0,false)；CEX 腿 CS=1 由 resolver 内部处理（与 D-062 同语义）。
// D-190：勿用于 sizing——notional 链 CS 单一来源=legContractSizeOf（USDPerLotForLeg 内部）。
// 本函数仅保留机会卡展示用途。
func (s *Service) OppContractSize(canonical, venueKey string) (float64, bool) {
	if s.oppContractSize == nil {
		return 0, false
	}
	return s.oppContractSize(canonical, venueKey)
}

// SetFreezeGuard 注入冻结腿预检窗口（D-155）。≤0 = 禁用（全放行）。
func (s *Service) SetFreezeGuard(d time.Duration) { s.freezeGuard = d }

// SetStepMeta 注入 binance:perp stepSize 权威元数据（D-127 P2）。nil = 仅用硬编码 fallback。
func (s *Service) SetStepMeta(m *cexStepMeta) { s.stepMeta = m }

// SetOkxLotSz 注入 okx:perp 腿最小张数 lotSz 查询（D-311 R4，instId→lotSz，instruments 同源缓存）。
// nil = 未接线 → okx:perp step=0 fail-closed。
func (s *Service) SetOkxLotSz(f func(instID string) (float64, bool)) { s.okxLotSz = f }

// SetSwapArbComboAllow 注入 SWAPARB 精确组合门禁 allowlist（D-159 W1a R1，手动入口同源）。
// nil/空 = 未接线（gate 层 fail-closed 兜底，手动入口不重复检查）。
func (s *Service) SetSwapArbComboAllow(allow []exec.ComboAllowSpec) {
	s.swapArbComboAllow.Store(&allow) // D-515 S5：热刷新写侧
}

// swapArbAllowSpecs 热读读侧（nil=未接线——gate 层兜底语义不动）。
func (s *Service) swapArbAllowSpecs() []exec.ComboAllowSpec {
	if v := s.swapArbComboAllow.Load(); v != nil {
		return *v
	}
	return nil
}

// SetXAssetComboAllow 注入 XASSET_CARRY 精确组合门禁 allowlist（D-311 S6，手动入口同源）。
// 零信任：空表 = 全拒（不是 SWAPARB 的 nil=未接线语义）。
func (s *Service) SetXAssetComboAllow(allow []exec.XAssetComboAllowSpec) { s.xassetAllow.Store(&allow) } // D-403 R2：热刷新写侧

// SetXAssetAllowEnvironment D-403 R2：白名单加载环境（approve 扇出 rows 单查用）。
func (s *Service) SetXAssetAllowEnvironment(env string) { s.xassetEnv = env }

// xassetAllowSpecs D-403 R2：手动执行链读侧（atomic Load；nil=未接线=空集全拒）。
func (s *Service) xassetAllowSpecs() []exec.XAssetComboAllowSpec {
	if v := s.xassetAllow.Load(); v != nil {
		return *v
	}
	return nil
}

// SetCarryRegime 注入 carry 入口 funding regime 持久性闸参数（D-328 S5，手动入口同源）。
// 零值 = fail-closed（XASSET_CARRY 全拒），装配缺接线不会静默放行。
func (s *Service) SetCarryRegime(cfg exec.CarryRegimeConfig) { s.carryRegime = cfg }

// SetEnvironment 注入执行环境固定值（D-159 W1a I6，手动入口同源）。
// 空 = 未配置（不检查，回退旧行为）。
func (s *Service) SetEnvironment(env string) { s.environment = env }

// SetGateChecker D-620⑦：注入真金单闸控制面（*gate.ControlPlane；nil=未接线全径 fail-closed）。
func (s *Service) SetGateChecker(gc GateChecker) { s.gateChecker = gc }

// SetAliasMap D-175：注入 venue→alias 缓存（main 启动时加载 ListAccountAliases）。
// nil/空 = 未接线（oppToProto alias 留空，前端回退 venueKey）。线程安全（RWMutex）。
func (s *Service) SetAliasMap(m map[string]string) {
	s.aliasMu.Lock()
	defer s.aliasMu.Unlock()
	s.aliasMap = m
}

// aliasForVenueKey D-175：从 venueKey="{venue}:{src}" 提取 venue 查 alias 缓存。
// venueKey 无冒号 → 整串当 venue 查；alias 空/未接线 → 返回空（前端回退 venueKey）。
func (s *Service) aliasForVenueKey(venueKey string) string {
	s.aliasMu.RLock()
	m := s.aliasMap
	s.aliasMu.RUnlock()
	if m == nil {
		return ""
	}
	return m[keydomain.BareOf(venueKey)]
}

// SetXAssetCarryLive D-340 S3：注入引擎当前净 carry 公开包装（autotrade.XAssetNetCarryPublic，
// 与退出判定 recalcXAssetNetCarry 同源；engine 后建故 setter 注入——装配锚见 d340 测试+setup 注入行）。
func (s *Service) SetXAssetCarryLive(fn func(ctx context.Context, posID int64) (float64, bool)) {
	s.xassetCarryLive = fn
}

// SetEconSources D-014 ④：注入 econ 同源——posFn=持仓快照（回本列与 harvestCheck 共一
// 结构）；entryFn=候选入场回本天数（pending_entries 回本估）。nil=未接线→缺省显示。
func (s *Service) SetEconSources(posFn func(ctx context.Context, posID int64) (econ.Snapshot, error),
	entryFn func(ctx context.Context, venueBuy, venueSell, canonBuy, canonSell string, legNotionalUSD float64) (float64, bool)) {
	s.econPosFn, s.econEntryFn = posFn, entryFn
}

// SetCmcLiveProvider D-236 变更9：注入 CMC ws LiveState 快照提供者
// （main 装配 ws.LiveState；nil=未接线→freshLive 全走 DB 兜底径）。
func (s *Service) SetCmcLiveProvider(fn func() cmc.LiveState) { s.cmcLiveProvider = fn }

// SetCmcWsHealth D-193-A/R9：注入 CMC ws 事件驱动健康检查（main 装配 ws.IsHealthy）。
func (s *Service) SetCmcWsHealth(fn func() bool) { s.cmcWsHealth = fn }
