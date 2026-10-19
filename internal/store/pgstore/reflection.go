// reflection.go —— settle 单消费者管线（PraisonAI 终稿①②合并票）：
// 「平仓→自省→复读判→冷却输入」单管线。
//
// ② 自省（reflection）：settle 事件双出口挂点——positions 走 PostSettleHook
// 既有钩（WritePositionReflection），paper 走 ExitPaperSpotCarry 内联写
// （单消费者同管双用：exit_reason 归因先于指纹消费）。
// 模板=固定确定性四段（存储终裁补钉：零 JSON——人读 text 模板即 schema；
// 机读件=typed 列 exit_kind/entry_expected_cost_bps）。硬坑遵行：预期值
// 一律读落库锚（expected_net_bps/entry_expected_cost_bps/entry_* 既有列），
// 禁后视重建=look-ahead 变体。
//
// ① 复读指纹：在 xasset_combo_cooldown.go——(combo_id,side,exit_reason) 第三
// 输入源，非策略退出（venue_starved/data_lost/manual）滤除不计。
package pgstore

import (
	"context"
	"fmt"
	"log"
	"time"

	"nox/internal/data/fact"
)

// WritePositionReflection ② 自省写径（PostSettleHook 形=func(ctx,posID) 的
// error 变体——装配侧 wrap 吞 err 记 log，钩链不可中断）。结算行落账后触发，
// 读仓落库锚+腿+as-of funding 回取组装四段模板写 positions.reflection。
// 幂等：重写覆盖（结算重跑场景下模板随最新落库值重算，锚列不变语义稳定）。
func (s *Store) WritePositionReflection(ctx context.Context, posID int64) error {
	var (
		expectedNetBps, entryCostBps, feesUSD, notional, realizedPnl *float64
		exitKind, ptype, oppID                                       string
		openedAt, closedAt                                           *time.Time
	)
	err := s.pool.QueryRow(ctx, `
		SELECT type, opportunity_id, expected_net_bps, entry_expected_cost_bps,
		       fees_usd, notional_usd, realized_pnl, COALESCE(exit_kind,''),
		       opened_at, closed_at
		FROM positions WHERE id = $1`, posID).Scan(
		&ptype, &oppID, &expectedNetBps, &entryCostBps,
		&feesUSD, &notional, &realizedPnl, &exitKind,
		&openedAt, &closedAt)
	if err != nil {
		return fmt.Errorf("reflection load pos %d: %w", posID, err)
	}
	// 入场时 funding 回取（as-of opened_at——时态读口，禁读现值重建）。
	entryFacts := "缺"
	if openedAt != nil {
		legs, lerr := s.ListPositionLegsByPhase(ctx, posID, "OPEN")
		if lerr == nil {
			var fs []string
			for _, l := range legs {
				rows, ferr := s.pool.Query(ctx, `
					SELECT value FROM facts
					WHERE kind = $1 AND venue = $2 AND symbol = $3 AND ts <= $4
					ORDER BY ts DESC LIMIT 1`, fact.KindFunding, l.Venue, l.Symbol, *openedAt)
				if ferr == nil {
					if rows.Next() {
						var v float64
						if rows.Scan(&v) == nil {
							fs = append(fs, fmt.Sprintf("%s:%s funding=%.4f", l.Venue, l.Symbol, v))
						}
					}
					rows.Close()
				}
			}
			if len(fs) > 0 {
				entryFacts = joinSemis(fs)
			}
		}
	}
	// 成本差=实收 fee 折 bps−预期成本 bps（预期值读落库锚）。
	costDiff := "缺（预期/实收未全锚）"
	var feeDevUSD *float64
	if feesUSD != nil && notional != nil && *notional > 0 && entryCostBps != nil {
		actualBps := *feesUSD / *notional * 1e4
		d := actualBps - *entryCostBps
		dev := *feesUSD - *entryCostBps**notional/1e4
		feeDevUSD = &dev
		costDiff = fmt.Sprintf("实收%.4fbps−预期%.4fbps=%+.4fbps", actualBps, *entryCostBps, d)
	}
	// 模型偏差=预期 PnL（bps×notional）vs 实结（realized_pnl）；归因=费差 USD
	// +滑点/时滞逐仓未记=如实缺（不虚构）。
	dev := "缺（预期/实结未全锚）"
	if expectedNetBps != nil && notional != nil && realizedPnl != nil {
		expUSD := *expectedNetBps / 1e4 * *notional
		feeAttr := "费差缺"
		if feeDevUSD != nil {
			feeAttr = fmt.Sprintf("费差%+.4fUSD", *feeDevUSD)
		}
		dev = fmt.Sprintf("预期%+.4fUSD vs 实结%+.4fUSD=%+.4fUSD（归因:%s/滑·时滞=未逐仓记）",
			expUSD, *realizedPnl, *realizedPnl-expUSD, feeAttr)
	}
	entryAssump := fmt.Sprintf("expected_net_bps=%s", fptr(expectedNetBps, "%.4f"))
	text := fmt.Sprintf("入场假设: %s｜facts@open=%s｜成本差: %s｜出场: %s｜模型偏差: %s",
		entryAssump, entryFacts, costDiff, orDash(exitKind), dev)
	_, err = s.pool.Exec(ctx, `UPDATE positions SET reflection = $2 WHERE id = $1`, posID, text)
	return err
}

// AnchorEntryExpectedCost ①② 前置锚：开仓时点把成本模型实测均值锁进
// entry_expected_cost_bps（reflection 只读落库值禁重建——look-ahead 硬坑）。
// 读 fin_cost_model 全 OPEN 腿逐腿 venue×structure 全天桶 fee 均值求和
// （fills>0 实测优先；任一腿无实测=NULL 如实缺——不占位虚报不估零）；
// IS NULL 守卫=首锚不覆（重入幂等）；旧单腿锚行留痕不回改。
// 调用点=pipeline_notional 双腿名义额落定后（venue/structure/notional 三要素齐）。
func (s *Store) AnchorEntryExpectedCost(ctx context.Context, posID int64) error {
	var (
		ptype    string
		notional *float64
	)
	err := s.pool.QueryRow(ctx, `
		SELECT type, notional_usd FROM positions WHERE id=$1`, posID).Scan(&ptype, &notional)
	if err != nil || notional == nil || *notional <= 0 {
		return err // 无名义=不锚（如实缺）
	}
	// 双腿聚合（refinement——旧径 LEFT JOIN LATERAL 首腿只锚单边 venue 费，
	// 跨所双腿仓 CEX+MT5 费率结构异=成本系统性低估→指纹净额偏乐观）：
	// 全 OPEN 腿逐腿 cost_model 均值求和；任一腿无实测=如实缺锚不记（禁估零）。
	legs, err := s.pool.Query(ctx, `
		SELECT venue, venue_type FROM position_legs
		WHERE position_id=$1 AND phase='OPEN' AND COALESCE(venue,'')<>''`, posID)
	if err != nil {
		return err
	}
	defer legs.Close()
	type legKey struct{ venue, vtype string }
	var legKeys []legKey
	for legs.Next() {
		var k legKey
		if err := legs.Scan(&k.venue, &k.vtype); err != nil {
			return err
		}
		legKeys = append(legKeys, k)
	}
	if err := legs.Err(); err != nil {
		return err
	}
	if len(legKeys) == 0 {
		return nil // 腿未齐=不锚（如实缺）
	}
	var totalFee float64
	for _, k := range legKeys {
		var avgFee *float64
		if err := s.pool.QueryRow(ctx, `
			SELECT SUM(fee_usd)::float8 / NULLIF(SUM(fills),0)
			FROM fin_cost_model
			WHERE venue=$1 AND venue_type=$2 AND structure=$3 AND bucket_hour=-1 AND fills>0`,
			k.venue, k.vtype, ptype).Scan(&avgFee); err != nil {
			return err
		}
		if avgFee == nil {
			return nil // 缺腿实测=如实缺锚不记（禁估零——禁线）
		}
		totalFee += *avgFee
	}
	bps := totalFee / *notional * 1e4
	_, err = s.pool.Exec(ctx, `
		UPDATE positions SET entry_expected_cost_bps=$2
		WHERE id=$1 AND entry_expected_cost_bps IS NULL`, posID, bps)
	return err
}

// paperReflection ② 自省·纸面出口模板（ExitPaperSpotCarry 内联写——单消费者
// 同管：exit_reason 写入同时组装，指纹消费即吃已归因值）。
// 四段同构 positions 模板：入场假设=entry_funding_ann/entry_basis_bps 落库锚；
// 成本差=est_round_trip_fee_usd（入场锁定预期）vs 实累计；出场=exit_reason；
// 模型偏差=预期 carry（funding_ann 折持有期）vs 实结（accrued+convergence）。
func paperReflection(p *paperReflectRow) string {
	entryAssump := fmt.Sprintf("side=%s structure=%s｜entry_funding_ann=%s｜entry_basis_bps=%s",
		p.Side, p.Structure, fptr(p.EntryFundingAnn, "%.4f%%"), fptr(p.EntryBasisBps, "%.4fbps"))
	costDiff := fmt.Sprintf("预期往返费=%s｜实累计=%s",
		fptr(p.EstRoundTripFeeUSD, "%.4fUSD"), fptr(p.AccruedUSD, "%+.4fUSD"))
	dev := "缺（预期/实结未全锚）"
	if p.EntryFundingAnn != nil && p.QtyUSD != nil && p.EntryTs != nil && p.ExitTs != nil {
		holdDays := p.ExitTs.Sub(*p.EntryTs).Hours() / 24
		if holdDays > 0 {
			expUSD := *p.EntryFundingAnn / 100 / 365 * holdDays * *p.QtyUSD
			var act float64
			if p.AccruedUSD != nil {
				act += *p.AccruedUSD
			}
			if p.ConvergenceUSD != nil {
				act += *p.ConvergenceUSD
			}
			dev = fmt.Sprintf("预期carry%+.4fUSD vs 实结%+.4fUSD=%+.4fUSD", expUSD, act, act-expUSD)
		}
	}
	return fmt.Sprintf("入场假设: %s｜成本差: %s｜出场: %s｜模型偏差: %s",
		entryAssump, costDiff, orDash(p.ExitReason), dev)
}

// writePaperReflection ExitPaperSpotCarry 内联写径——exit_reason 落库同拍
// 组装四段模板（单消费者同管：指纹消费即吃已归因值）；败只记 log 不回滚。
func (s *Store) writePaperReflection(ctx context.Context, id int64) {
	var pr paperReflectRow
	var et time.Time
	if err := s.pool.QueryRow(ctx, `
		SELECT side, structure, COALESCE(exit_reason,''), entry_funding_ann,
		       entry_basis_bps, est_round_trip_fee_usd, accrued_usd,
		       convergence_usd, qty_usd, entry_ts, exit_ts
		FROM paper_spot_carry WHERE id = $1`, id).Scan(
		&pr.Side, &pr.Structure, &pr.ExitReason, &pr.EntryFundingAnn,
		&pr.EntryBasisBps, &pr.EstRoundTripFeeUSD, &pr.AccruedUSD,
		&pr.ConvergenceUSD, &pr.QtyUSD, &pr.EntryTs, &et); err != nil {
		return
	}
	pr.ExitTs = &et
	if _, err := s.pool.Exec(ctx,
		`UPDATE paper_spot_carry SET reflection = $2 WHERE id = $1`,
		id, paperReflection(&pr)); err != nil {
		log.Printf("[pgstore] paper %d reflection write: %v", id, err)
	}
}

// paperReflectRow paper 自省行料（ExitPaperSpotCarry 退出后回填读）。
type paperReflectRow struct {
	Side, Structure, ExitReason    string
	EntryFundingAnn, EntryBasisBps *float64
	EstRoundTripFeeUSD, AccruedUSD *float64
	ConvergenceUSD, QtyUSD         *float64
	EntryTs, ExitTs                *time.Time
}

func fptr(v *float64, format string) string {
	if v == nil {
		return "缺"
	}
	return fmt.Sprintf(format, *v)
}

func orDash(s string) string {
	if s == "" {
		return "缺"
	}
	return s
}

func joinSemis(parts []string) string {
	out := parts[0]
	for _, p := range parts[1:] {
		out += ";" + p
	}
	return out
}
