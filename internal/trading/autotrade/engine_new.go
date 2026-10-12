package autotrade

import (
	"time"

	"nox/internal/data/bus"
	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"
)

// New 创建自动交易引擎（从 engine.go 提取避免超 300 行）。
func New(cfg gate.Config, store *pgstore.Store, service engineService, oppTopic *bus.Topic[pgstore.Opportunity], quotes *bus.QuoteBus) *Engine {
	cp := gate.NewControlPlane(store)
	cp.SetLiveGateChecker(cfg.LiveGateChecker) // D-195-C1：LIVE 导出门注入（nil 安全）
	e := &Engine{
		cfg:              cfg,
		store:            store,
		service:          service,
		oppTopic:         oppTopic,
		quotes:           quotes,
		gate:             gate.NewGate(store, cfg, cp),
		fundingNeedSent:  make(map[string]string),
		fundingBreach:    make(map[int64]int),     // D-133 T1：PERP_FUNDING 去抖计数
		legSuspend:       make(map[int64]bool),    // D-495 S3：腿可平性 suspend 转场去重
		disasterBreach:   make(map[int64]int),     // D-378b S1：灾难带去抖计数
		cexBasisAnchor:   make(map[int64]float64), // D-113：CEX 腿-共识锚（disasterMu 域，lazy-init 防呆仍在）
		mtDivergeFlag:    make(map[int64]bool),    // D-113：MT 偏离告警 episode 去重（同上）
		csMissCounts:     make(map[string]int),    // D-140 S2：CS-miss 聚合
		csMissSent:       make(map[string]time.Time),
		marginAlertSent:  make(map[string]time.Time), // D-144 ②：保证金告警去重
		revivalAlertSent: make(map[string]time.Time), // D-256-A S5：复活扳机告警去重
		kickCh:           make(chan struct{}, 1),     // D-107 P4：buffered(1) 非阻塞 send = 事件合并
		quoteCh:          make(chan bus.Quote, 256),  // D-004 P1#4：positionLoop quote 快路帧（满丢帧不阻塞）
		carryKick:        make(chan struct{}, 1),     // D-004 P1#4：carry facts 写后 → 权威全扫
		mrKick:           make(chan struct{}, 1),     // D-469c S1
		factsKick:        make(chan struct{}, 1),     // D-469c S3
		swapQuoteCh:      make(chan bus.Quote, 256),  // D-004 P1#5：swapArb quote 快路帧（满丢帧不阻塞）
	}
	// 开户门槛档——dashboard.MarginFloorCheck 注入（nil service=未接线不检查）。
	// 同点注入统一资金读口（gate_margin CEX Src 闸；与 dashboard 共享一实例）。
	if service != nil {
		e.gate.SetMarginFloorChecker(service)
		e.gate.SetCapitalView(service.CapitalView())
	}
	e.gate.SetVenueAlias(nil) // D-556：缺省无别名（main SetGateVenueAlias 装配闭包）
	// 热重载指针初值=启动加载结果（cfg.XAssetAllowCombos，setup 期 xassetAllowFromTable 产出）。
	initAllow := cfg.XAssetAllowCombos
	e.xassetAllow.Store(&initAllow)
	// swaparb 快照初值=boot 表加载结果（setup 期 swapArbAllowFromTable 产出）。
	e.swapArb.Store(&gate.SwapArbComboSnapshot{Allow: cfg.SwapArbAllowCombos, Deny: cfg.SwapArbDenyCombos})
	e.gate.SetSwapArbSnapshot(&e.swapArb)
	// 初值=setup 表加载结果（allowRulesFromTable 产出，同 xasset 装配序）。
	e.allowRules.Store(&allowRules{venues: cfg.AllowVenues, canonicals: cfg.AllowCanonicals})
	return e
}

// SetGateVenueAlias D-556：透传 gate 账号别名闭包（资金拒单白话文案用；装配面，
// main setup 启动时 ListAccountAliases 一把加载注入）。
func (e *Engine) SetGateVenueAlias(aliasOf func(vk string) string) {
	e.gate.SetVenueAlias(aliasOf)
}
