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
	store           *pgstore.Store
	credentialsFunc func(ctx context.Context) bool // 第 4 条 readiness 检查函数（S3 注入；nil=占位 true）
}

// NewLiveGateChecker 创建 LiveGate 检查器。
// credentialsFunc 为 nil 时第 4 条默认 true（占位，S3 接入 readiness 后替换）。
// snapshotTTL 参数随 capital 面退役移除。
func NewLiveGateChecker(store *pgstore.Store, credentialsFunc func(ctx context.Context) bool) *LiveGateChecker {
	return &LiveGateChecker{store: store, credentialsFunc: credentialsFunc}
}

// Check 执行 LiveGate 8 条检查。
// 步骤 5 首版：只做检查逻辑，不写入 live_gates 表（LIVE 未开放）。
// 任何一条失败 → Passed=false，Reasons 收集所有失败原因。
func (lg *LiveGateChecker) Check(ctx context.Context, accountKey, strategy, environment string) LiveGateCheckResult {
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

	// 第 3 条：账户环境与策略环境一致
	// 查询 account 的 is_real，与 environment 比对
	if environment != "" && accountKey != "" {
		isReal, err := lg.store.GetAccountIsReal(ctx, accountKey)
		if err != nil {
			reasons = append(reasons, fmt.Sprintf("account is_real query failed: %v", err))
		} else {
			wantReal := environment == "live"
			if isReal != wantReal {
				reasons = append(reasons, fmt.Sprintf("environment mismatch: env=%s but account is_real=%v", environment, isReal))
			} else {
				result.EnvConsistent = true
			}
		}
	} else {
		result.EnvConsistent = true // 未配置 environment 不检查
	}

	// 第 4 条：token/凭据/executor/broker/readiness 全部正常
	// S3 接入 readiness checker；credentialsFunc=nil 时占位 true（步骤 5 LIVE 未开放，此条不影响 fail-closed 结论）
	if lg.credentialsFunc != nil {
		result.CredentialsReady = lg.credentialsFunc(ctx)
		if !result.CredentialsReady {
			reasons = append(reasons, "credentials/readiness check failed")
		}
	} else {
		result.CredentialsReady = true
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
