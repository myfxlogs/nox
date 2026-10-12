package gate

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"arb/internal/platform/combo"
	"arb/internal/portfolio/capitalview"
	"arb/internal/store/pgstore"
)

// Gate 统一开仓门禁（D-072 §3.1）。三 goroutine 串行化。
// 防重改 DB 实时派生——每次 Submit 实时查 ListActivePositions 派生组合键（治手动旁路）。
type Gate struct {
	mu    sync.Mutex
	store *pgstore.Store
	cfg   Config
	// 执行控制面（execution_controls 是执行模式唯一真相源）。
	controlPlane *ControlPlane
	// disabled 拒绝 stdout 1h 去重（复用 D-144 ② per-key 1h 模式）。
	// DB 落库保留全量（审计完整性不动），只压 stdout。内存，重启重置。
	lastDisabledLog   map[string]time.Time
	lastDisabledLogMu sync.Mutex
	// 开户门槛档检查器（dashboard.MarginFloorCheck 注入；nil=未接线不检查）。
	marginFloor marginFloorChecker
	// 执行面统一资金读口（与 dashboard 共享一实例；nil=未接线跳过 Src 闸——
	// dashboard 侧 marginFloorEval 仍保有 CEX 执行面 fail-closed 兜底）。
	capView *capitalview.View
	// 账号别名读口（main 装配 ListAccountAliases 闭包；nil=回落 bare 键）。
	venueAlias func(vk string) string
	// 资金拒单业主告警节流（per combo key ≥6h 至多一条 warn；内存态重启重置，
	// autotrade_log 全量审计不受节流影响）。
	lastMarginWarn   map[string]time.Time
	lastMarginWarnMu sync.Mutex
	// 拒单签名熔断（key=dedupKey|class；内存态重启重置可接受）。
	breaker   map[string]*rejectEntry
	breakerMu sync.Mutex
	swapArb   *atomic.Pointer[SwapArbComboSnapshot] // D-515 S5：表热读快照共享指针（nil=回退 cfg）
}

// NewGate 创建门禁。
// 新增 controlPlane 参数（autotrade.New 内部自建，签名扩展第 3 参数）。
func NewGate(store *pgstore.Store, cfg Config, controlPlane *ControlPlane) *Gate {
	return &Gate{
		store:           store,
		cfg:             cfg,
		controlPlane:    controlPlane,
		lastDisabledLog: make(map[string]time.Time),
		lastMarginWarn:  make(map[string]time.Time),
	}
}

// Submit 统一开仓入口。全程持锁串行化（设计 §3.1）。
// 拒绝路径也落 autotrade_logs（三路径统一审计）。
func (g *Gate) Submit(ctx context.Context, intent *TradeIntent) GateResult {
	g.mu.Lock()
	defer g.mu.Unlock()

	// 统一执行模式检查（execution_controls 是唯一真相源）
	// 首版只用 global scope；account 级覆盖留后续工作包。
	if g.controlPlane != nil {
		mode, allowed, cpReason, cpErr := g.controlPlane.CheckGate(ctx)
		if cpErr != nil || !allowed {
			g.rejectLog(ctx, *intent, fmt.Sprintf("control plane: %v (mode=%s)", cpErr, mode), false)
			return GateResult{Status: "REJECTED", Reason: "execution mode blocked: " + cpReason}
		}
	}

	// 读取当前状态
	stateRow, err := g.store.GetAutotradeState(ctx)
	if err != nil {
		// fail-closed（D-072 复审修正）：状态不可知 = 拒绝开仓，禁止默认 RUNNING 放行
		g.rejectLog(ctx, *intent, fmt.Sprintf("get state: %v", err), false)
		return GateResult{Status: "REJECTED", Reason: "get autotrade state failed"}
	}
	state := stateRow.State

	// 检查 1b: 残腿/降级拒开（CLOSING∪LEGGED 非空 或 venue 平仓通道降级）。
	// 先于策略禁用/日亏——残腿窗口内任何新仓都在扩大裸露风险。
	if res, rejected := g.residualLegGate(ctx, intent); rejected {
		return res
	}

	// 事故仓停新开（D-030 硬线）——残腿闸同族前置语义的完整集：
	// 事故族仓（含已平 incident_close/legged_unwind）未 ack 前任何新仓都扩大裸露风险。
	if res, rejected := g.incidentGate(ctx, intent); rejected {
		return res
	}

	// 高影响财历事件执行禁区——窗内 high 事件 ∧ currency∈禁区集 → REJECTED。
	// fail-closed（查询失败=拒）。reason 固定前缀 "macro blackout"（selfwatch 计数依赖）。
	if res, rejected := g.macroBlackoutGate(ctx, intent); rejected {
		return res
	}

	// 检查 2: 策略禁用（fail-closed：查询失败 = 拒绝）
	disabled, err := g.store.IsStrategyDisabled(ctx, intent.Strategy)
	if err != nil {
		g.rejectLog(ctx, *intent, fmt.Sprintf("check strategy disabled: %v", err), false)
		return GateResult{Status: "REJECTED", Reason: "strategy disabled check failed"}
	}

	// 检查 3: 日亏损（fail-closed：查询失败 = 拒绝）
	dailyPnl, err := g.store.DailyRealizedPnl(ctx)
	if err != nil {
		g.rejectLog(ctx, *intent, fmt.Sprintf("daily pnl: %v", err), false)
		return GateResult{Status: "REJECTED", Reason: "daily pnl check failed"}
	}

	// 检查 4+6: 活跃持仓数 + 同组合防重（D-080 R1：DB 实时派生，一条 SQL 替代 CountOpenPositions + 内存 openCombos）
	// fail-closed：查询失败 = 拒绝（对齐检查 2-3 风格）
	activePositions, err := g.store.ListActivePositions(ctx)
	if err != nil {
		g.rejectLog(ctx, *intent, fmt.Sprintf("list active positions: %v", err), false)
		return GateResult{Status: "REJECTED", Reason: "active positions check failed"}
	}
	openCount := len(activePositions)
	intentCombo := combo.FromOppID(intent.DedupKey)
	comboAlreadyOpen := false
	for _, p := range activePositions {
		if p.OpportunityID == "" {
			continue
		}
		if combo.MatchOppID(p.OpportunityID, intentCombo) {
			comboAlreadyOpen = true
			break
		}
	}

	// 检查 7: 止损重进冷却——closed 持仓抑制（stop/manual × 冷却窗 × 同 ComboKey）。
	// gate 统一闸单点覆盖全 intent 来源（扫描/机会/MEANREVERT）；harvest/carry_death/NULL 豁免。
	// fail-closed：查询失败=拒绝（与检查 4+6 同风格）；StopReentryCooldown<=0=检查关闭。
	stopHit, stopKind, stopAgo, err := g.stopCooldownCheck(ctx, intentCombo)
	if err != nil {
		g.rejectLog(ctx, *intent, fmt.Sprintf("list cooled exits: %v", err), false)
		return GateResult{Status: "REJECTED", Reason: "cooled exits check failed"}
	}

	// D-378a S4 检查 8: 组合级止损保险丝（C1）——同 ComboKey stop/exec_drift 平仓 ≥ComboLossLimit
	// in ComboLossWindow → 拒。与 cooldown 各查各的（kind 集与窗口不同，合并查询会把 kind 过滤
	// 抬进应用层，破坏「SQL 侧过滤、应用层零判断」语义）。fail-closed 同 cooldown。
	comboHit, comboCount, err := g.comboLossCheck(ctx, intentCombo)
	if err != nil {
		g.rejectLog(ctx, *intent, fmt.Sprintf("combo loss check: %v", err), false)
		return GateResult{Status: "REJECTED", Reason: "combo loss check failed"}
	}

	verdict := gateDecision(GateInput{
		State:        state,
		Strategy:     intent.Strategy,
		Disabled:     disabled,
		DailyPnl:     dailyPnl,
		MaxDailyLoss: g.cfg.MaxDailyLoss,
		OpenCount:    openCount,
		MaxPositions: g.cfg.MaxPositions,
		NotionalUSD:  intent.NotionalUSD,
		MaxNotional:  g.cfg.MaxNotional,
		// SWAPARB 豁免 notional cap：D-096 判定书（净算月~$110=年化1.8%无杠杆，不注资）+
		// 自管 sizing（SwapArbBareCap 1 手 + minNotional skip；swap 收租需整手，
		// MaxNotional 单笔风险 cap 语义不适用——整手收租非单笔风险敞口）。
		// XASSET_CARRY 同豁免——intent.NotionalUSD 是 sizing 预算语义，黄金对实额可超
		// 单笔 cap；敞口闸=combo spec MaxGrossUSD（pipeline checkXAssetComboAllow 事后校验终审）。
		SkipNotional: intent.Strategy == "SWAPARB" || intent.Strategy == "CARRY" || intent.Strategy == "XASSET_CARRY",
		DedupHit:     comboAlreadyOpen,
		// 止损重进冷却命中（组合级历史抑制，DedupHit 同族）
		StopCooldownHit:  stopHit,
		StopCooldownKind: stopKind,
		StopCooldownAgo:  stopAgo,
		// 组合级止损保险丝命中（C1，count 供审计 reason）
		ComboLossHit:   comboHit,
		ComboLossCount: comboCount,
	})

	// 精确组合门禁（SWAPARB/CARRY 共用，gateDecision 后、Execute 前拦截）。
	// CARRY 合并 SWAPARB 统一门禁栈——comboAllowVerdict/gross split 对 CARRY 同样生效。
	// gateDecision 已过（state=RUNNING、未禁用、未超仓、未超 notional、未 dedup）才进入此检查。
	// 非 RUNNING 状态（CLOSE_ONLY 等）已在 gateDecision 拒绝，不会到此。
	// 判据族搬移——块体→gate_rules.go comboAllowGate（纯搬移零逻辑变更）。
	if res, rejected := g.comboAllowGate(ctx, intent, activePositions); rejected {
		return res
	}
	// environment/is_real 一致性检查（所有策略，gateDecision 后）。
	// 校验 venue 的 broker_accounts.is_real 与 g.cfg.Environment 一致：
	//   is_real=true ↔ environment='live' / is_real=false ↔ environment='demo'
	//   is_real=nil（未知）→ fail-closed 拒绝（不允许未知环境开仓）。
	// Environment 空 = 未配置（不检查，回退旧行为）。
	if g.cfg.Environment != "" {
		if rv := g.environmentIsRealCheck(ctx, *intent); !rv.Pass {
			g.rejectLog(ctx, *intent, rv.Reason, false)
			return GateResult{Status: "REJECTED", Reason: rv.Reason}
		}
	}

	// 构造日志条目（三路径统一审计）
	logEntry := &pgstore.AutotradeLog{
		OpportunityID: intent.DedupKey,
		Strategy:      intent.Strategy,
		Canonical:     intent.Canonical,
		VenueBuy:      intent.VenueBuy,
		VenueSell:     intent.VenueSell,
		Qty:           intent.Qty,
		NetBps:        intent.NetBps,
	}

	if !verdict.Pass {
		// 检查 3 触发 DAILY_LOSS_HALT
		if verdict.Reason != "" && state == StateRunning && dailyPnl <= -g.cfg.MaxDailyLoss {
			haltedOn := time.Now().UTC().Truncate(24 * time.Hour)
			// 状态写入失败显式处理——日志 + critical alert（alert 失败也 log）。
			// 不增加 inMemoryHalt（Gate 每次 Submit 重新读状态/日亏/策略禁用，内存 latch 无恢复协议+重复防线）。
			if err := g.store.SetAutotradeState(ctx, StateDailyLossHalt, verdict.Reason, &haltedOn); err != nil {
				msg := fmt.Sprintf("自动交易日亏停机状态写入失败（保守按停机处理，下一轮自动重读）：%v", err)
				log.Printf("[autotrade] %s", msg)
				if alertErr := g.store.InsertAlert(ctx, nil, "critical", msg); alertErr != nil {
					log.Printf("[autotrade] DAILY_LOSS_HALT alert insert also failed: %v", alertErr)
				}
			}
		}
		// 同组合防重拒绝原因覆盖（gateDecision 返回通用 "duplicate dedup key"）
		if comboAlreadyOpen {
			verdict.Reason = "combo already open"
		}
		// disabled 触发的拒绝 stdout 1h 去重（combo 覆盖 reason 时不去重）。
		g.rejectLog(ctx, *intent, verdict.Reason, disabled && !comboAlreadyOpen)
		return GateResult{Status: "REJECTED", Reason: verdict.Reason}
	}

	// LIVE 人工开关——verdict.Pass=true 后才检查（disabled/dedup/daily_loss 拒绝时不查）。
	// is_real=true 账户 + LiveTrade=false → 拒绝开仓。demo（is_real=false）不受限制。
	// fail-closed：is_real 查询失败 → 拒绝。
	if !g.cfg.LiveTrade {
		if rv := g.liveGateCheck(ctx, *intent); !rv.Pass {
			g.rejectLog(ctx, *intent, rv.Reason, false)
			return GateResult{Status: "REJECTED", Reason: rv.Reason}
		}
	}

	// swap 偏差拦截——同 venue 已有 OPEN 持仓的 broker 实际 swap vs facts 规格值偏差超阈值→拒绝。
	// 复用 ListActivePositions（line 91 已查）反查同 venue OPEN 持仓，取 DailySwapAccrued + LatestFacts 实时算偏差。
	// fail-closed：查询失败→拒绝。首次开仓无历史数据→放行（走 S3 零值校验）。
	if g.cfg.SwapDeviationGatePct > 0 {
		if rv := g.swapDeviationGateCheck(ctx, *intent, activePositions); !rv.Pass {
			g.rejectLog(ctx, *intent, rv.Reason, false)
			return GateResult{Status: "REJECTED", Reason: rv.Reason}
		}
	}

	// 开户门槛档——venue 可用保证金 < 新仓所需（隐含保证金率口径）→ 拒 + info 告警。
	// 未接线（nil checker）跳过；数据源缺由 checker 内部 fail-open 处理（监控件不阻断开仓链）。
	if rv := g.marginFloorGateCheck(ctx, *intent); !rv.Pass {
		g.rejectLog(ctx, *intent, rv.Reason, false)
		return GateResult{Status: "REJECTED", Reason: rv.Reason}
	}

	// 签名熔断——活动窗内只跳执行（上方闸检查已照跑，fail-closed 不破），
	// 每窗只写 1 行 PAUSED 审计行（洪峰归零）。
	if be := g.breakerCheck(intent.DedupKey); be != nil {
		g.breakerLogOnce(ctx, intent, be)
		log.Printf("[autotrade] breaker skip %s class=%s resume=%s", intent.DedupKey, be.Class, be.ResumeAt.Format("15:04"))
		return GateResult{Status: "REJECTED", Reason: "breaker " + be.Class}
	}

	// 执行 + 结果收尾（分类落库/熔断记账/审计，D-053 拆 gate_helpers.go 纯搬移）。
	status, msg, posID := intent.Execute(ctx)
	return g.finalizeSubmit(ctx, intent, logEntry, status, msg, posID)
}
