package pgstore

import (
	"context"
	"fmt"
	"math"
	"time"

	"nox/internal/review/verification"
)

// VerificationStat 对应 verification_stats 表（D-086）。
type VerificationStat struct {
	Vkey         string
	Samples      int
	ExpectedUsd  float64
	RealizedUsd  float64
	LastSampleAt *time.Time
	UpdatedAt    time.Time
}

// UpsertVerificationSample 增量 UPSERT 验证统计（平仓挂点+回填脚本共用）。
// samples+1 / expected_usd+Δ / realized_usd+Δ / last_sample_at=closedAt。
// 幂等性由调用方保证（平仓挂点每 position 调一次；回填脚本先清后建）。
func (s *Store) UpsertVerificationSample(ctx context.Context, vkey string, expectedUsd, realizedUsd float64, closedAt time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO verification_stats (vkey, samples, expected_usd, realized_usd, last_sample_at, updated_at)
		VALUES ($1, 1, $2, $3, $4, now())
		ON CONFLICT (vkey) DO UPDATE SET
			samples = verification_stats.samples + 1,
			expected_usd = verification_stats.expected_usd + $2,
			realized_usd = verification_stats.realized_usd + $3,
			last_sample_at = $4,
			updated_at = now()
	`, vkey, expectedUsd, realizedUsd, closedAt)
	if err != nil {
		return fmt.Errorf("upsert verification sample: %w", err)
	}
	return nil
}

// UpdateVerificationStatForPosition 平仓增量挂点：从 position+legs 派生 vkey/expectedUsd/notional/holdDays，
// 调 UpsertVerificationSample。样本纳入条件（D-086 实施细化）：
//   - realized_pnl IS NOT NULL（可信口径）
//   - expected_usd > 0（负期望开仓=事故不入统计；SWAPARB 历史负快照行天然排除）
//
// notional=Σ OPEN FILLED 腿 qty×CS×open价（CS 经 broker_symbol_map，CEX 腿 CS=1）；
// holdDays=（closed_at−opened_at）/24h，opened_at NULL → min(position_legs.ts)。
// vkey 从 opportunity_id 派生（combo 包纯函数，无环依赖）。
func (s *Store) UpdateVerificationStatForPosition(ctx context.Context, p *Position) error {
	if p.RealizedPnl == nil {
		return nil
	}
	vkey := verification.VkeyFromOppID(p.OpportunityID)
	if vkey == "" {
		return nil // 无法派生 vkey（异常 oppID），跳过不报错
	}

	// 取 OPEN FILLED 腿算 notional + holdDays 兜底
	legs, err := s.ListPositionLegs(ctx, p.ID)
	if err != nil {
		return fmt.Errorf("list legs for verification: %w", err)
	}

	// canonical 从 oppID 解析（与 autotrade.canonicalFromOppID 等价但本包不依赖 autotrade）
	canonical := canonicalFromOppIDLocal(p.OpportunityID)

	notional := 0.0
	var minLegTs time.Time
	for _, l := range legs {
		if l.Phase != "OPEN" || l.Status != "FILLED" {
			continue
		}
		if l.PriceActual != nil && *l.PriceActual > 0 && l.Qty > 0 {
			cs := 1.0 // CEX 腿 CS=1
			if c, ok, _ := s.ContractSizeByCanonical(ctx, l.Venue, canonical); ok && c > 0 {
				cs = c
			}
			notional += l.EffectiveQty() * cs * *l.PriceActual // D-311 R6：成交口径（CEX=基础币）
		}
		if minLegTs.IsZero() || l.Ts.Before(minLegTs) {
			minLegTs = l.Ts
		}
	}

	// holdDays
	var holdDays float64
	if p.ClosedAt != nil {
		openedAt := p.OpenedAt
		if openedAt == nil && !minLegTs.IsZero() {
			openedAt = &minLegTs
		}
		if openedAt != nil {
			holdDays = p.ClosedAt.Sub(*openedAt).Hours() / 24
		}
	}

	// §3.4 typed 新签名：netBps + swapBpsDaily 按字段取值
	netBps := p.ExpectedNetBps
	swapBpsDaily := 0.0
	if p.ExpectedSwapBpsDaily != nil {
		swapBpsDaily = *p.ExpectedSwapBpsDaily
	}
	expectedUsd := verification.ExpectedUsd(p.Type, netBps, swapBpsDaily, notional, holdDays)
	if expectedUsd <= 0 {
		return nil // 负期望/零期望不入统计
	}

	closedAt := time.Now()
	if p.ClosedAt != nil {
		closedAt = *p.ClosedAt
	}
	return s.UpsertVerificationSample(ctx, vkey, expectedUsd, *p.RealizedPnl, closedAt)
}

// VerificationFor 查询 vkey 的验证统计（engine 发布门用）。
// samples=总样本数；ratio=realized_usd/expected_usd（expected_usd≤0 → NaN）；
// fresh=last_sample_at > now()-windowDays（窗口外或无样本 → false）。
// 无行 → samples=0, ratio=NaN, fresh=false（V0）。
func (s *Store) VerificationFor(ctx context.Context, vkey string, windowDays int) (samples int, ratio float64, fresh bool, err error) {
	var (
		smpl       int
		expUsd     float64
		realUsd    float64
		lastSample *time.Time
	)
	err = s.pool.QueryRow(ctx, `
		SELECT samples, expected_usd, realized_usd, last_sample_at
		FROM verification_stats WHERE vkey = $1
	`, vkey).Scan(&smpl, &expUsd, &realUsd, &lastSample)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return 0, math.NaN(), false, nil
		}
		return 0, math.NaN(), false, fmt.Errorf("verification for %s: %w", vkey, err)
	}
	ratio = math.NaN()
	if expUsd > 0 {
		ratio = realUsd / expUsd
	}
	fresh = false
	if lastSample != nil && windowDays > 0 {
		fresh = time.Since(*lastSample) < time.Duration(windowDays)*24*time.Hour
	}
	return smpl, ratio, fresh, nil
}

// ListVerificationStats 返回全部验证统计行（前端折叠区/归因诊断用）。
// 按 last_sample_at DESC 排序（最新验证在前）。
func (s *Store) ListVerificationStats(ctx context.Context) ([]VerificationStat, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT vkey, samples, expected_usd, realized_usd, last_sample_at, updated_at
		FROM verification_stats
		ORDER BY last_sample_at DESC NULLS LAST
	`)
	if err != nil {
		return nil, fmt.Errorf("list verification stats: %w", err)
	}
	defer rows.Close()
	var out []VerificationStat
	for rows.Next() {
		var v VerificationStat
		if err := rows.Scan(&v.Vkey, &v.Samples, &v.ExpectedUsd, &v.RealizedUsd, &v.LastSampleAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ResetVerificationStats 清空 verification_stats（回填脚本幂等用：先清后建）。
func (s *Store) ResetVerificationStats(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM verification_stats`)
	if err != nil {
		return fmt.Errorf("reset verification stats: %w", err)
	}
	return nil
}

// canonicalFromOppID 从 opportunity_id 解析 canonical（格式 PREFIX:SYMBOL:...）。
// 与 autotrade.canonicalFromOppID 等价但本包不依赖 autotrade（避免环依赖）。
func canonicalFromOppIDLocal(oppID string) string {
	// 剥离旧 tick 尾时间戳
	ck := oppID
	if idx := lastIndexColon(oppID); idx >= 0 {
		tail := oppID[idx+1:]
		if isAllDigitsLocal(tail) {
			ck = oppID[:idx]
		}
	}
	// 取前两段：strategy:canonical
	parts := splitColon(ck)
	if len(parts) >= 2 {
		return parts[1]
	}
	return ""
}

func lastIndexColon(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ':' {
			return i
		}
	}
	return -1
}

func isAllDigitsLocal(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func splitColon(s string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	parts = append(parts, s[start:])
	return parts
}
