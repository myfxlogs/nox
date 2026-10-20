// perf_baseline.go —— D-535：业绩基线锚读 + 本金口径（D-534：USDT+USD）收益链。
// 业绩真值 = 本金池变动 − 外部充提净额（充正提负）；内部划转（direction='transfer'
// D-539）不进本金池变动外部项。锚键存 app_config（baseline_usd/baseline_at/
// baseline_ccy/perf_flow_provisional），写死后改须新 D#。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// PerfBaselineKey* 基线锚键（D-536 总览页契约名 baseline_usd/baseline_at）。
const (
	PerfBaselineUSDKey  = "baseline_usd"
	PerfBaselineAtKey   = "baseline_at"
	PerfBaselineCcyKey  = "baseline_ccy"
	PerfFlowProvisional = "perf_flow_provisional"
)

// PerfBaseline 业绩基线锚（OK=false=未配置——展示面如实缺）。
type PerfBaseline struct {
	OK              bool
	BaselineUSD     float64
	BaselineAt      time.Time
	CcyScope        string // 默认 "USDT+USD"
	FlowProvisional bool   // 净流扣减 provisional 标（perf_flow_provisional 键）
}

// PerfBaseline 锚读（键缺→OK=false 非错误；值坏→error fail-closed）。
func (s *Store) PerfBaseline(ctx context.Context) (*PerfBaseline, error) {
	usd, ok, err := s.AppConfigFloat(ctx, PerfBaselineUSDKey)
	if err != nil {
		return nil, err
	}
	var atStr string
	err = s.pool.QueryRow(ctx, `SELECT value FROM app_config WHERE key=$1`, PerfBaselineAtKey).Scan(&atStr)
	if err != nil {
		atStr = "" // 行缺按无锚处理（下判）
	}
	if !ok || atStr == "" {
		return &PerfBaseline{}, nil
	}
	at, perr := time.Parse(time.RFC3339, atStr)
	if perr != nil {
		return nil, fmt.Errorf("baseline_at parse %q: %w", atStr, perr)
	}
	b := &PerfBaseline{OK: true, BaselineUSD: usd, BaselineAt: at, CcyScope: "USDT+USD"}
	var ccy string
	if err := s.pool.QueryRow(ctx, `SELECT value FROM app_config WHERE key=$1`, PerfBaselineCcyKey).Scan(&ccy); err == nil && ccy != "" {
		b.CcyScope = ccy
	}
	var prov string
	if err := s.pool.QueryRow(ctx, `SELECT value FROM app_config WHERE key=$1`, PerfFlowProvisional).Scan(&prov); err == nil {
		b.FlowProvisional = prov == "true"
	}
	return b, nil
}

// NetExternalFlowSince 外部充提净额（direction deposit 正 / withdraw 负；transfer
// 内部划转不计——本金池总量不因内部移动变）。ccy 只认 USDT/USD（D-534 本金口径）；
// skipped=非 U 币种充提行数（披露面——BTC 提币不进本金流不算损益）。
func (s *Store) NetExternalFlowSince(ctx context.Context, since time.Time) (net float64, skipped int, err error) {
	// D-633 归一化：单扫 venue 分组+FILTER 一次出 net/skipped/byVenue（原双查询同表重扫合流）。
	byVenue, net, skipped, err := s.externalFlowScan(ctx, since)
	_ = byVenue
	return net, skipped, err
}

// externalFlowScan 单扫 fin_venue_io——GROUP BY venue 行内 FILTER 拆
// U 币种净额/非 U 行数/byVenue map（三调用面共用底扫，谓词单源）。
func (s *Store) externalFlowScan(ctx context.Context, since time.Time) (map[string]float64, float64, int, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT venue,
		       COALESCE(SUM(CASE WHEN direction='deposit' THEN amount ELSE -amount END)
		                FILTER (WHERE ccy IN ('USDT','USD')), 0),
		       COUNT(*) FILTER (WHERE ccy NOT IN ('USDT','USD'))
		FROM fin_venue_io
		WHERE ts >= $1 AND direction <> 'transfer'
		GROUP BY venue`, since)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("external flow scan: %w", err)
	}
	defer rows.Close()
	byVenue := map[string]float64{}
	var net float64
	var skipped int
	for rows.Next() {
		var v string
		var amt float64
		var sk int
		if err := rows.Scan(&v, &amt, &sk); err != nil {
			return nil, 0, 0, err
		}
		byVenue[v] = amt
		net += amt
		skipped += sk
	}
	return byVenue, net, skipped, rows.Err()
}

// NetExternalFlowByVenueSince 外部充提净额按 venue 拆（同 NetExternalFlowSince 口径——
// deposit 正 / withdraw 负 / transfer 不计 / 只认 USDT+USD；总览「总投入」卡组成明细）。
func (s *Store) NetExternalFlowByVenueSince(ctx context.Context, since time.Time) (map[string]float64, error) {
	byVenue, _, _, err := s.externalFlowScan(ctx, since)
	return byVenue, err
}

// PerfReport 基线后业绩（本金口径单源结果——财务页/日报/总览共读）。
type PerfReport struct {
	BaselineUSD     float64
	BaselineAt      time.Time
	PrincipalNowUSD float64 // USDT+USD 桶 NAV 合计（本金池现值）
	PoolDeltaUSD    float64 // PrincipalNow − Baseline
	NetExternalUSD  float64 // 基线后外部充提净额（充正提负）
	SkippedIO       int     // 非 U 币种充提行数（披露）
	PerfUSD         float64 // PoolDelta − NetExternal（真业绩）
	Days            float64 // 基线起算天数
	PerfAnnPct      float64 // perf/days×365/baseline×100（days>0 才填）
	FlowProvisional bool
}

// PerfSinceBaseline 业绩计算：本金池=NAV 的 USDT+USD 两桶（D-534；跨桶相加属本金口径
// 钦定——非「跨币加总」违例，USD/USDT 同记帐单位族 1:1）。读败任一环 fail-closed 返错。
func (s *Store) PerfSinceBaseline(ctx context.Context, now time.Time) (*PerfReport, error) {
	b, err := s.PerfBaseline(ctx)
	if err != nil {
		return nil, err
	}
	if !b.OK {
		return nil, nil // 无锚=无业绩面（如实缺）
	}
	nav, err := s.ComputeNav(ctx, now)
	if err != nil {
		return nil, fmt.Errorf("perf nav: %w", err)
	}
	principal := nav.ByCcy["USDT"].Nav + nav.ByCcy["USD"].Nav
	netFlow, skipped, err := s.NetExternalFlowSince(ctx, b.BaselineAt)
	if err != nil {
		return nil, err
	}
	r := &PerfReport{
		BaselineUSD:     b.BaselineUSD,
		BaselineAt:      b.BaselineAt,
		PrincipalNowUSD: principal,
		PoolDeltaUSD:    principal - b.BaselineUSD,
		NetExternalUSD:  netFlow,
		SkippedIO:       skipped,
		PerfUSD:         principal - b.BaselineUSD - netFlow,
		Days:            now.Sub(b.BaselineAt).Hours() / 24,
		FlowProvisional: b.FlowProvisional,
	}
	if r.Days > 0 && b.BaselineUSD > 0 {
		r.PerfAnnPct = r.PerfUSD / r.Days * 365 / b.BaselineUSD * 100
	}
	return r, nil
}
