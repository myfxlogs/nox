// D-182 S3 拆分：从 setup.go 提取 readyzCheck（readiness 分级检查）。
// D-195-C1 S3：readiness 分级（process/db/source/facts/executor/settlement/live_gate）。D-120：capital_snapshot 层退役。
package main

import (
	"context"
	"time"

	"nox/internal/data/bus"
	"nox/internal/data/fact"
	"nox/internal/platform/config"
	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"

	"nox/internal/trading/exec"
)

// ReadinessLayer D-195-C1 §8.4：readiness 单层状态。
type ReadinessLayer struct {
	Name     string `json:"name"`
	Ready    bool   `json:"ready"`
	Age      string `json:"age,omitempty"`       // 数据年龄
	MaxAge   string `json:"max_age,omitempty"`   // TTL
	SourceTs string `json:"source_ts,omitempty"` // 数据源时间戳
	Reason   string `json:"reason,omitempty"`    // 不可交易原因
}

// ReadinessReport D-195-C1 §8.4：readiness 报告（D-120：capital_snapshot 层随资本面退役移除）。
type ReadinessReport struct {
	ProcessAlive    ReadinessLayer `json:"process_alive"`
	DatabaseReady   ReadinessLayer `json:"database_ready"`
	SourceReady     ReadinessLayer `json:"source_ready"`
	FactsFresh      ReadinessLayer `json:"facts_fresh"`
	ExecutorReady   ReadinessLayer `json:"executor_ready"`
	SettlementReady ReadinessLayer `json:"settlement_ready"`
	LiveGateReady   ReadinessLayer `json:"live_gate_ready"`
	Tradable        bool           `json:"tradable"` // 所有关键层 ready 才 true（live_gate 除外）
}

// readyzCheck D-195-C1 S3：readiness 8 层分级检查。
// 返回 ReadinessReport。DB fail → Tradable=false（HTTP 503 由调用方判定）。
//
// 层级：
//   - process_alive：总是 ready（进程在跑才能响应）
//   - database_ready：ping DB
//   - source_ready：QuoteBus 最新 quote age < DetectorFreshnessTTL
//   - facts_fresh：最新 fact age < SwapFactMaxAge
//   - executor_ready：execReg 注册了至少 1 个执行器
//   - settlement_ready：DB 连接 + autotrade_state 可查
//   - live_gate_ready：live_gates active 行+env==live+readiness 全过（D-620）
func readyzCheck(ctx context.Context, st *pgstore.Store, qbus *bus.QuoteBus, cfg *config.Config, execReg *exec.Registry, liveGate *gate.LiveGateChecker) ReadinessReport {
	var report ReadinessReport

	// process_alive：总是 ready
	report.ProcessAlive = ReadinessLayer{Name: "process_alive", Ready: true}

	// database_ready：ping DB
	dbLayer := ReadinessLayer{Name: "database_ready"}
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := st.Pool().Ping(pingCtx); err != nil {
		dbLayer.Ready = false
		dbLayer.Reason = "db ping failed: " + err.Error()
	} else {
		dbLayer.Ready = true
	}
	report.DatabaseReady = dbLayer

	// source_ready：QuoteBus 最新 quote age < DetectorFreshnessTTL
	sourceLayer := ReadinessLayer{Name: "source_ready", MaxAge: cfg.DetectorFreshnessTTL.String()}
	quotes := qbus.AllLatest()
	if len(quotes) == 0 {
		sourceLayer.Ready = false
		sourceLayer.Reason = "no quotes"
	} else {
		newest := quotes[0].Time
		for _, q := range quotes[1:] {
			if q.Time.After(newest) {
				newest = q.Time
			}
		}
		age := time.Since(newest)
		sourceLayer.Age = age.String()
		sourceLayer.SourceTs = newest.Format(time.RFC3339)
		if age > cfg.DetectorFreshnessTTL {
			sourceLayer.Ready = false
			sourceLayer.Reason = "quotes stale"
		} else {
			sourceLayer.Ready = true
		}
	}
	report.SourceReady = sourceLayer

	// facts_fresh：最新 fact age < SwapFactMaxAge
	factsLayer := ReadinessLayer{Name: "facts_fresh", MaxAge: cfg.MTData.SwapFactMaxAge.String()}
	facts, err := st.LatestFactsByKind(ctx, fact.KindSwapLong)
	if err != nil || len(facts) == 0 {
		factsLayer.Ready = false
		factsLayer.Reason = "no facts"
		if err != nil {
			factsLayer.Reason = "fact query failed: " + err.Error()
		}
	} else {
		newest := facts[0].Ts
		for _, f := range facts[1:] {
			if f.Ts.After(newest) {
				newest = f.Ts
			}
		}
		age := time.Since(newest)
		factsLayer.Age = age.String()
		factsLayer.SourceTs = newest.Format(time.RFC3339)
		if age > cfg.MTData.SwapFactMaxAge {
			factsLayer.Ready = false
			factsLayer.Reason = "facts stale"
		} else {
			factsLayer.Ready = true
		}
	}
	report.FactsFresh = factsLayer

	// executor_ready：execReg 注册了至少 1 个执行器
	execLayer := ReadinessLayer{Name: "executor_ready"}
	if execReg == nil {
		execLayer.Ready = false
		execLayer.Reason = "executor registry not configured"
	} else if execReg.Len() == 0 {
		execLayer.Ready = false
		execLayer.Reason = "no executors registered"
	} else {
		execLayer.Ready = true
	}
	report.ExecutorReady = execLayer

	// settlement_ready：DB 连接 + autotrade_state 可查
	settleLayer := ReadinessLayer{Name: "settlement_ready"}
	if !dbLayer.Ready {
		settleLayer.Ready = false
		settleLayer.Reason = "db not ready"
	} else {
		_, err := st.GetAutotradeState(ctx)
		if err != nil {
			settleLayer.Ready = false
			settleLayer.Reason = "autotrade_state query failed: " + err.Error()
		} else {
			settleLayer.Ready = true
		}
	}
	report.SettlementReady = settleLayer

	// live_gate_ready：LiveGate 检查（D-620——live_gates active 行+环境+readiness 全过才 ready）
	lgLayer := ReadinessLayer{Name: "live_gate_ready"}
	if liveGate == nil {
		lgLayer.Ready = false
		lgLayer.Reason = "live gate checker not configured"
	} else {
		lgResult := liveGate.Check(ctx)
		if lgResult.Passed {
			lgLayer.Ready = true
		} else {
			lgLayer.Ready = false
			lgLayer.Reason = gate.FormatLiveGateReasons(lgResult.Reasons)
		}
	}
	report.LiveGateReady = lgLayer

	// Tradable：所有关键层 ready（live_gate 除外——授权前永 false 但 PAPER/TEST 可交易，且 GrantLive 前置自调 readiness 含闸=自锁死）
	report.Tradable = report.DatabaseReady.Ready && report.SourceReady.Ready &&
		report.FactsFresh.Ready &&
		report.ExecutorReady.Ready && report.SettlementReady.Ready
	return report
}
