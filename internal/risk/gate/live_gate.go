package gate

import (
	"context"
	"fmt"
	"strings"
	"time"

	"nox/internal/store/pgstore"
)

// LiveGateCheckResult D-195 §3.2：LiveGate 8 条检查结果。
type LiveGateCheckResult struct {
	Passed  bool
	Reasons []string // 未通过的原因列表
	// 各条检查的详细状态
	HasGateRecord     bool // 第 1 条
	OperatorConfirmed bool // 第 2 条
	EnvConsistent     bool // 第 3 条
	CredentialsReady  bool // 第 4 条
	NotExpired        bool // 第 8 条（原 5-7 条 capital 面随 D-120 退役）
}

// LiveGateChecker D-195 §3.2：LiveGate 8 条检查器。
type LiveGateChecker struct {
	store       *pgstore.Store
	readinessFn func(ctx context.Context) bool // readiness 实查（D-620④：nil 占位退役——nil=fail-closed）
	environment string                         // D-620 核验补正Ⓐ：构造注入，Check 直比 =="live"，空串拒
}

// NewLiveGateChecker D-620④：readinessFn=readyz Tradable 注入；environment=构造注入（空串=未配置）。
func NewLiveGateChecker(store *pgstore.Store, readinessFn func(ctx context.Context) bool, environment string) *LiveGateChecker {
	return &LiveGateChecker{store: store, readinessFn: readinessFn, environment: environment}
}

// Environment 返回构造注入的环境值（GrantLive check_snapshot 用）。
func (lg *LiveGateChecker) Environment() string { return lg.environment }

// Check 执行 LiveGate 8 条检查。
// 步骤 5 首版：只做检查逻辑，不写入 live_gates 表（LIVE 未开放）。
// 任何一条失败 → Passed=false，Reasons 收集所有失败原因。
func (lg *LiveGateChecker) Check(ctx context.Context) LiveGateCheckResult {
	var result LiveGateCheckResult
	var reasons []string

	// 第 1 条：有明确的 LiveGate 持久记录
	// 步骤 5 无 live_gates 行 → 直接 fail（LIVE 未开放）
	gate, err := lg.store.GetActiveLiveGate(ctx, "global", "")
	if err != nil {
		reasons = append(reasons, fmt.Sprintf("live gate query failed: %v", err))
	} else if gate == nil {
		reasons = append(reasons, "no active live gate record")
	} else {
		result.HasGateRecord = true
		// 第 2 条：操作者明确确认目标账户和策略
		result.OperatorConfirmed = gate.Operator != ""
		if !result.OperatorConfirmed {
			reasons = append(reasons, "operator not confirmed")
		}
		// 第 8 条：LiveGate 未过期、未撤销
		result.NotExpired = gate.ExpiresAt == nil || gate.ExpiresAt.After(time.Now())
		if !result.NotExpired {
			reasons = append(reasons, "live gate expired")
		}
	}

	// 第 3 条：环境判据=构造注入值直比 "live"（D-620 核验补正Ⓐ——空串=未配置 fail-closed，
	// 禁「未配置不检查」短路放行形态）。
	if lg.environment == "live" {
		result.EnvConsistent = true
	} else {
		reasons = append(reasons, fmt.Sprintf("environment not configured as live (got %q)", lg.environment))
	}

	// 第 4 条：token/凭据/executor/broker/readiness 全部正常
	// S3 接入 readiness checker；credentialsFunc=nil 时占位 true（步骤 5 LIVE 未开放，此条不影响 fail-closed 结论）
	if lg.readinessFn == nil {
		reasons = append(reasons, "readiness checker not wired")
	} else {
		result.CredentialsReady = lg.readinessFn(ctx)
		if !result.CredentialsReady {
			reasons = append(reasons, "credentials/readiness check failed")
		}
	}

	// 原第 5/6/7 条（capital snapshot / hypothesis / capital 回撤）随
	// 资本面退役整链删除——其表已 DROP，保留判据=永红死闸。

	result.Passed = result.HasGateRecord && result.OperatorConfirmed && result.EnvConsistent &&
		result.CredentialsReady && result.NotExpired
	result.Reasons = reasons
	return result
}

// FormatLiveGateReasons D-195-C1：拼接 LiveGate 失败原因（ControlPlane.CheckGate + readiness 用）。
func FormatLiveGateReasons(reasons []string) string {
	return strings.Join(reasons, "; ")
}
