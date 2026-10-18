package gate

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"nox/internal/store/pgstore"
)

// ExecutionMode D-195 §3.1：执行模式枚举。
const (
	ModeReadOnly = "READ_ONLY"
	ModePaper    = "PAPER"
	ModeTest     = "TEST"
	// D-620①：LIVE=唯一真金导出位——只许 GrantLive 写入（live_gates active 行=唯一授权记录），
	// SetMode("LIVE") 直调拒绝。
	ModeLive = "LIVE"
)

// KillSwitchReason D-195 §3.3：KillSwitch 触发原因。
type KillSwitchReason struct {
	Reason    string
	Actor     string
	Timestamp time.Time
}

// ControlPlane D-195 §3.1/§3.4：执行控制面服务。
// execution_controls 是执行模式唯一持久化来源；
// autotrade_state 只能降级权限，不能提升执行权限。
type ControlPlane struct {
	store           *pgstore.Store
	liveGateChecker *LiveGateChecker // D-195-C1：LIVE 模式导出门（nil=LIVE 直接拒绝）
}

// NewControlPlane 创建控制面服务。
func NewControlPlane(store *pgstore.Store) *ControlPlane {
	return &ControlPlane{store: store}
}

// SetLiveGateChecker D-195-C1：注入 LiveGate 检查器（LIVE 模式导出门，nil 安全）。
// 不改 NewControlPlane 签名（已有调用方不动）；autotrade.New 内部调用此方法注入。
func (cp *ControlPlane) SetLiveGateChecker(lg *LiveGateChecker) {
	if lg != nil {
		cp.liveGateChecker = lg
	}
}

// GetMode 读取当前执行模式（首版只用 global scope；account 级覆盖留后续工作包）。
// 无 global 控制行 → READ_ONLY（fail-closed）。
func (cp *ControlPlane) GetMode(ctx context.Context) (string, error) {
	c, err := cp.store.GetExecutionControl(ctx, "global", "")
	if err != nil {
		return "", err
	}
	if c == nil {
		return ModeReadOnly, nil // fail-closed
	}
	return c.Mode, nil
}

// SetMode 设置执行模式（写入 execution_controls + 事件）。
// 步骤 5 只允许 READ_ONLY/PAPER/TEST；LIVE 写入 → 返回错误。
// scopeType 只支持 "global"（首版）；scopeKey=""。
func (cp *ControlPlane) SetMode(ctx context.Context, mode, actor, reason string) error {
	if mode == ModeLive {
		return fmt.Errorf("control plane: mode LIVE is grant-only (use GrantLive)")
	}
	if mode != ModeReadOnly && mode != ModePaper && mode != ModeTest {
		return fmt.Errorf("control plane: mode %s not allowed in step 5 (only READ_ONLY/PAPER/TEST)", mode)
	}
	// D-620③ 降级即撤权：离开 LIVE 先撤 live_gates active 行——kill 后回 LIVE 须重新 Grant 带新证据。
	if rerr := cp.store.RevokeLiveGate(ctx, "global", "", "mode downgrade by "+actor); rerr != nil {
		return fmt.Errorf("control plane: revoke live gate on downgrade: %w", rerr)
	}

	// 读取当前 mode（用于事件 from_mode）
	fromMode, _ := cp.GetMode(ctx)

	ctrl := &pgstore.ExecutionControl{
		ScopeType:   "global",
		ScopeKey:    "",
		Mode:        mode,
		Enabled:     true,
		ApprovedBy:  actor,
		ConfirmedAt: ptrTimeNow(),
		Reason:      reason,
	}
	event := &pgstore.ExecutionControlEvent{
		ScopeType: "global",
		ScopeKey:  "",
		FromMode:  fromMode,
		ToMode:    mode,
		Actor:     actor,
		Reason:    reason,
		EventKey:  fmt.Sprintf("setmode-%s-%d", mode, time.Now().UnixNano()),
	}
	return cp.store.UpsertExecutionControl(ctx, ctrl, event)
}

// TriggerKillSwitch 触发 KillSwitch（设 mode=READ_ONLY + enabled=false + 记录原因）。
func (cp *ControlPlane) TriggerKillSwitch(ctx context.Context, actor, reason string) error {
	// D-620③ kill 覆盖真金写面全集——先撤 live_gates active 行（无 active=no-op），
	// 事故后不续旧权，回 LIVE 必须重新 Grant。
	if rerr := cp.store.RevokeLiveGate(ctx, "global", "", "kill switch: "+reason); rerr != nil {
		return fmt.Errorf("control plane: revoke live gate on kill: %w", rerr)
	}
	fromMode, _ := cp.GetMode(ctx)
	ctrl := &pgstore.ExecutionControl{
		ScopeType:  "global",
		ScopeKey:   "",
		Mode:       ModeReadOnly,
		Enabled:    false,
		ApprovedBy: actor,
		Reason:     reason,
	}
	event := &pgstore.ExecutionControlEvent{
		ScopeType: "global",
		ScopeKey:  "",
		FromMode:  fromMode,
		ToMode:    ModeReadOnly,
		Actor:     actor,
		Reason:    "kill switch: " + reason,
		EventKey:  fmt.Sprintf("killswitch-trigger-%d", time.Now().UnixNano()),
	}
	return cp.store.UpsertExecutionControl(ctx, ctrl, event)
}

// ResolveKillSwitch 解除 KillSwitch（显式人工，写入原因/操作者/时间/复核证据）。
// 解除后 mode 保持 READ_ONLY（需显式 SetMode 提升）。
func (cp *ControlPlane) ResolveKillSwitch(ctx context.Context, actor, reason, evidenceRef string) error {
	ctrl := &pgstore.ExecutionControl{
		ScopeType:  "global",
		ScopeKey:   "",
		Mode:       ModeReadOnly,
		Enabled:    true,
		ApprovedBy: actor,
		Reason:     "kill switch resolved: " + reason,
	}
	event := &pgstore.ExecutionControlEvent{
		ScopeType:   "global",
		ScopeKey:    "",
		FromMode:    ModeReadOnly,
		ToMode:      ModeReadOnly,
		Actor:       actor,
		Reason:      "kill switch resolved: " + reason,
		EvidenceRef: evidenceRef,
		EventKey:    fmt.Sprintf("killswitch-resolve-%d", time.Now().UnixNano()),
	}
	return cp.store.UpsertExecutionControl(ctx, ctrl, event)
}

// IsKillSwitchActive 检查 KillSwitch 是否激活（enabled=false）。
func (cp *ControlPlane) IsKillSwitchActive(ctx context.Context) (bool, error) {
	c, err := cp.store.GetExecutionControl(ctx, "global", "")
	if err != nil {
		return false, err
	}
	if c == nil {
		return false, nil
	}
	return !c.Enabled, nil
}

// CheckGate D-195 §3.4：统一入口检查（首版只用 global scope）。
// 返回 (mode, allowed, reason, err)：
// - mode=READ_ONLY 或 KillSwitch 激活 → allowed=false
// - mode=PAPER/TEST → allowed=true（但 environment 隔离由调用方检查）
// - 查询失败 → fail-closed（allowed=false）
func (cp *ControlPlane) CheckGate(ctx context.Context) (mode string, allowed bool, reason string, err error) {
	c, qerr := cp.store.GetExecutionControl(ctx, "global", "")
	if qerr != nil {
		// fail-closed
		return ModeReadOnly, false, "query failed: " + qerr.Error(), qerr
	}
	if c == nil {
		// 无控制行 → READ_ONLY fail-closed
		return ModeReadOnly, false, "no execution control row (default READ_ONLY)", nil
	}
	mode = c.Mode
	if !c.Enabled {
		return mode, false, "kill switch active", nil
	}
	if mode == ModeReadOnly {
		return mode, false, "mode is READ_ONLY", nil
	}
	if mode == ModePaper || mode == ModeTest {
		return mode, true, "allowed", nil
	}
	// LIVE 模式导出门——LiveGate 8 条检查。
	if mode == ModeLive {
		if cp.liveGateChecker == nil {
			return mode, false, "LIVE mode but no live gate checker configured", nil
		}
		lgResult := cp.liveGateChecker.Check(ctx)
		if !lgResult.Passed {
			return mode, false, "live gate check failed: " + FormatLiveGateReasons(lgResult.Reasons), nil
		}
		return mode, true, "LIVE allowed by live gate", nil
	}
	// 未知 mode → fail-closed
	return mode, false, "unknown mode: " + mode, nil
}

// ptrTimeNow 返回 time.Now() 的指针。
func ptrTimeNow() *time.Time {
	t := time.Now()
	return &t
}

// GrantLive D-620②：LIVE 授权——唯一写 mode=LIVE 的径。
// 前置：environment=='live' 构造值非空、readiness Tradable、operator/reason/evidenceRef 非空、expiresAt>now。
// 落账=pgstore.GrantLiveGate 单事务（撤旧+插新+mode=LIVE+事件 to_mode=LIVE）。
// checkSnapshot=readiness 报告+前置结果 JSON；config_digest 留空如实（无 digest 源不编）。
func (cp *ControlPlane) GrantLive(ctx context.Context, operator, reason, evidenceRef string, expiresAt time.Time, readiness TradableReport) error {
	if cp.liveGateChecker == nil {
		return fmt.Errorf("control plane: live gate checker not wired")
	}
	if operator == "" || reason == "" || evidenceRef == "" {
		return fmt.Errorf("control plane: grant live requires operator/reason/evidence_ref")
	}
	if !expiresAt.After(time.Now()) {
		return fmt.Errorf("control plane: grant live expires_at must be in the future")
	}
	if !readiness.Tradable {
		return fmt.Errorf("control plane: grant live refused — readiness not tradable")
	}
	snap, _ := json.Marshal(map[string]any{
		"prechecks": map[string]any{
			"environment": cp.liveGateChecker.Environment(),
			"readiness":   readiness,
		},
	})
	fromMode, _ := cp.GetMode(ctx)
	now := time.Now()
	gate := &pgstore.LiveGate{
		ScopeType:     "global",
		ScopeKey:      "",
		Operator:      operator,
		ConfirmedAt:   now,
		ExpiresAt:     &expiresAt,
		EvidenceRef:   evidenceRef,
		CheckSnapshot: snap,
	}
	ctrl := &pgstore.ExecutionControl{
		ScopeType:   "global",
		ScopeKey:    "",
		Mode:        ModeLive,
		Enabled:     true,
		ApprovedBy:  operator,
		ConfirmedAt: &now,
		ExpiresAt:   &expiresAt,
		Reason:      reason,
	}
	event := &pgstore.ExecutionControlEvent{
		ScopeType:   "global",
		ScopeKey:    "",
		FromMode:    fromMode,
		ToMode:      ModeLive,
		Actor:       operator,
		Reason:      reason,
		EvidenceRef: evidenceRef,
		EventKey:    fmt.Sprintf("grantlive-%d", now.UnixNano()),
	}
	return cp.store.GrantLiveGate(ctx, gate, ctrl, event)
}

// TradableReport D-620②：GrantLive 前置 readiness 报告（注入 Grant 时快照 check_snapshot）。
type TradableReport struct {
	Tradable bool   `json:"tradable"`
	Detail   string `json:"detail,omitempty"`
}
