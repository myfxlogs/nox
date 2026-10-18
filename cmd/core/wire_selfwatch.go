// wire_selfwatch.go —— D-033：系统自盯汇报面装配（日报/Stage 闸/周报/数据面告警）。
// 调度=看门狗定点唤醒（同 macro/repoReminder 先例），数据全在触发时刻单查 DB；
// 周报=D-032 ④ 移入本面（autotrade.BuildRiskReport 文案单点不动）。
package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"nox/internal/ir/notify"
	"nox/internal/platform/safego"
	"nox/internal/research/scan"
	"nox/internal/risk/gate"
	"nox/internal/risk/selfwatch"
	"nox/internal/store/pgstore"
	"nox/internal/trading/autotrade"

	"github.com/jackc/pgx/v5/pgxpool"
)

// probeAnalystRO D-059 ②：analyst 只读链路探针——热读 app_secrets NOX_AI_PG_DSN
// （轮换后未重启即探真值）→ pgx SELECT 1。DSN 缺=nil（功能未启用静默跳过，非失败）。
// R2 ①：parse 失败不携带原文（pgxpool.New 错误透 DSN 全串含密码——固定文案丢弃 err，
// DSN 形态坏无诊断价值）；连接/查询错误不经 DSN 原文（pgx 只回 host/user/database）。
func probeAnalystRO(ctx context.Context, st *pgstore.Store) error {
	secrets, err := st.LoadAppSecrets(ctx)
	if err != nil {
		return fmt.Errorf("app_secrets load: %w", err)
	}
	dsn := secrets["NOX_AI_PG_DSN"]
	if dsn == "" {
		return nil // NOX_AI_PG_DSN 未配置=query_ro 功能未启用，不告警
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return errors.New("ro dsn parse failed（DSN 原文不回显）")
	}
	defer pool.Close()
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var one int
	return pool.QueryRow(pctx, "SELECT 1").Scan(&one)
}

// startSelfwatch 启动自盯 goroutine。feishu 未启用时仍跑（推送静默跳过，
// 报告照常落 alerts+docs/报告/+日志——缺发送目标不等于缺报告）。
func startSelfwatch(ctx context.Context, st *pgstore.Store, feishu *notify.FeishuClient, capitalBaseUSD float64, cp *gate.ControlPlane) {
	safego.Go("main:selfwatch", func() {
		selfwatch.Run(ctx, selfwatch.Deps{
			Store: st,
			EvalPaper: func(ctx context.Context, rows []pgstore.PaperSpotCarry, since, now time.Time) (*scan.PaperWindowEval, error) {
				return scan.EvalPaperCohort(ctx, st, rows, since, now)
			},
			Weekly: func(ctx context.Context, now time.Time) (string, error) {
				return autotrade.BuildRiskReport(ctx, st, cp, capitalBaseUSD, now)
			},
			KillActive:     cp.IsKillSwitchActive,
			LiveGateStatus: cp.LiveGateStatus,
			CapitalBase:    capitalBaseUSD,
			Notify:         feishu,
			ChatID:         st.FeishuChatID,
			// D-059 ②：analyst 只读链路探针（热读 app_secrets 真值——boot cfg 是快照，
			// 凭据轮换后未重启也能探到库内最新 DSN 的真实连通性）。
			ProbeAnalystRO: func(ctx context.Context) error {
				return probeAnalystRO(ctx, st)
			},
			// D-051③：CEX 纸面入口拦截分布（同实例 sink——wire_cex_paper 注入 opts）。
			CexSkipStats: cexPaperSkips.Snapshot,
		})
	})
}
