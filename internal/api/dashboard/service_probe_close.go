// service_probe_close.go —— D-047③④：探针到期平仓 + exit_kind='probe_expired' + D-013 账本。
// ExpireDueProbes 为 posMon 唤醒入口：定时器只负责叫醒，本函数查 DB 实时态决策；
// 逐仓 ClaimClose 原子领取（与手动 ClosePosition/对账路径不打架）→ 既有
// bm.CloseOrder 路径平（MT=OrderClose / CEX=反向 PlaceOrder）→ CLOSE 腿+cashflows+
// realized_pnl+settlement 全走 D-013 既有写面。
package dashboard

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"nox/internal/data/keydomain"
	"nox/internal/store/pgstore"
)

// probeCloseFailRetryWindow 平仓失败重试告警去重窗（探针仓回 OPEN 后逐次重试）。
const probeCloseFailRetryWindow = 30 * time.Minute

// NextProbeExpiryAt 下一次探针唤醒时刻（posMon 定时器臂用；nil=无在途探针）。
// R2①：读全部在途探针（OPEN/OPENING/CLOSING，auto_close_at 非空）——
// 未到期 → auto_close_at；已到期/卡态 → now+30min（与重试/重告警 dedup 同节奏，
// 禁 d=0 热循环；LEGGED 不入——PROBE 单腿无此态）。
func (s *Service) NextProbeExpiryAt(ctx context.Context) (*time.Time, error) {
	rows, err := s.store.ListProbes(ctx, true)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var next *time.Time
	for _, r := range rows {
		if r.AutoCloseAt == nil {
			continue
		}
		switch r.Status {
		case "OPEN", "OPENING", "CLOSING":
		default:
			continue
		}
		wake := *r.AutoCloseAt
		if !wake.After(now) {
			wake = now.Add(probeCloseFailRetryWindow)
		}
		if next == nil || wake.Before(*next) {
			w := wake
			next = &w
		}
	}
	return next, nil
}

// ExpireDueProbes 到期探针平仓扫描（事件驱动：posMon/posChange/定时器唤醒后调，
// 判定全查 DB 当前态）。逐仓独立失败不阻断后续。
func (s *Service) ExpireDueProbes(ctx context.Context) {
	if s.store == nil || s.bm == nil {
		return
	}
	due, err := s.store.ListDueProbes(ctx, time.Now())
	if err != nil {
		log.Printf("[probe] due scan: %v", err)
		return
	}
	for _, p := range due {
		if err := s.closeProbe(ctx, p); err != nil {
			log.Printf("[probe] expire pos=%d: %v", p.ID, err)
		}
	}
	// R1④：到期但卡 OPENING/CLOSING（venue 未确认/平仓在途）——critical 30min 去重，
	// 不改其状态（venue 侧真相未明，待对账/人工收编；LEGGED 不入——单腿即 OPEN 态）。
	stuck, err := s.store.ListStuckDueProbes(ctx, time.Now())
	if err != nil {
		log.Printf("[probe] stuck scan: %v", err)
		return
	}
	for _, p := range stuck {
		msg := fmt.Sprintf("[probe] 探针到期仍 %s 待对账 pos=%d", p.Status, p.ID)
		if err := s.store.InsertAlertDedup(ctx, "critical", msg, probeCloseFailRetryWindow); err != nil {
			log.Printf("[probe] pos=%d stuck alert: %v", p.ID, err)
		}
	}
}

// closeProbe 单仓到期平仓：claim → bm.CloseOrder → 落 CLOSE 腿（commission 现金流
// 自动写入）→ 净额合成 → CLOSED+probe_expired → settlement（skipped 行，不入统计）。
// venue 侧失败 → 回 OPEN 等下轮重试（dedup 告警 30min）。
func (s *Service) closeProbe(ctx context.Context, p pgstore.ProbeRow) error {
	if p.VenueKey == "" {
		return fmt.Errorf("pos=%d 无 OPEN 腿（venue_key 缺）", p.ID)
	}
	if err := s.store.ClaimClose(ctx, p.ID); err != nil {
		// ErrClaimFailed=已被平仓路径领取/状态迁移——非错误，跳过。
		if !errors.Is(err, pgstore.ErrClaimFailed) {
			return fmt.Errorf("claim close: %w", err)
		}
		return nil
	}
	ticket := p.Symbol // CEX：聚合仓 ticket=Symbol（bm.CloseOrder 快照匹配口径）
	if keydomain.IsMTKey(p.VenueKey) {
		ticket = p.OrderID // MT：leg.order_id==broker ticket（annotatePositionID 同口径）
	}
	if ticket == "" {
		s.revertProbeOpen(ctx, p.ID, "票号/symbol 缺（腿行无 order_id+symbol）")
		return fmt.Errorf("pos=%d close ticket missing", p.ID)
	}
	fill, err := s.bm.CloseOrder(ctx, p.VenueKey, ticket)
	if err != nil {
		s.revertProbeOpen(ctx, p.ID, fmt.Sprintf("到期平仓失败（venue 侧）: %v", err))
		return fmt.Errorf("pos=%d close order: %w", p.ID, err)
	}
	// CLOSE 腿落库（FILLED；CreatePositionLeg 内 EnsureLegCommissionCashflow 写 leg: 键行）。
	closeSide := "SELL"
	if p.Side == "SELL" {
		closeSide = "BUY"
	}
	qty := fill.Qty
	if qty <= 0 {
		qty = p.LegQty
	}
	now := time.Now()
	feeUSD := fill.FeeUSD
	leg := &pgstore.PositionLeg{
		PositionID: p.ID, LegNo: 1, Phase: "CLOSE",
		Venue: p.VenueKey, Account: keydomain.BareOf(p.VenueKey),
		Symbol: p.Symbol, Side: closeSide, Qty: qty,
		PriceActual: &fill.Price, OrderID: fill.OrderID, Status: "FILLED",
		AttemptNo: 1, FilledQty: &qty, AvgFillPrice: &fill.Price,
		FeeUSD: &feeUSD, SubmittedAt: &now, Ts: now,
	}
	if err := s.store.CreatePositionLeg(ctx, leg); err != nil {
		// venue 已平而腿落库失败——仓仍在 CLOSING：critical + 停在此态待对账/人工。
		s.probeCritical(ctx, p.ID, fmt.Sprintf("到期平仓已成交（ticket=%s px=%.6g）但 CLOSE 腿落库失败: %v", ticket, fill.Price, err))
		return fmt.Errorf("pos=%d close leg persist: %w", p.ID, err)
	}
	return s.finalizeProbeClose(ctx, p.ID)
}

// finalizeProbeClose 净额合成 + CLOSED + exit_kind='probe_expired' + settlement。
// D-013：净额=PositionNetUSD 单源（价差腿差 + funding/swap + commission 现金流）。
func (s *Service) finalizeProbeClose(ctx context.Context, posID int64) error {
	net, err := s.store.PositionNetUSD(ctx, posID, nil)
	if err != nil {
		s.probeCritical(ctx, posID, fmt.Sprintf("净额合成失败（仓留 CLOSING）: %v", err))
		return fmt.Errorf("pos=%d net pnl: %w", posID, err)
	}
	feesUSD, feeQuality := s.probeFeesUSD(ctx, posID)
	if err := s.store.ClosePosition(ctx, posID, net, feesUSD, feeQuality); err != nil {
		s.probeCritical(ctx, posID, fmt.Sprintf("CLOSED 标记失败: %v", err))
		return fmt.Errorf("pos=%d close mark: %w", posID, err)
	}
	if err := s.store.UpdatePositionExitKind(ctx, posID, pgstore.ExitKindProbeExpired); err != nil {
		log.Printf("[probe] pos=%d exit_kind probe_expired write: %v", posID, err)
	}
	if s.settler != nil {
		if err := s.settler.Settle(ctx, posID); err != nil {
			log.Printf("[probe] pos=%d settle: %v（skipped 行可重试）", posID, err)
		}
	}
	// D-048②：探针提案 running→closed（手动开的仓无关联提案=静默零行）。
	if err := s.store.ProbeProposalCloseByPosition(ctx, posID); err != nil {
		log.Printf("[probe] pos=%d proposal close sync: %v", posID, err)
	}
	// D-048③：取证报告触发（注入钩自决同步/异步；LLM 延迟不阻塞平仓面）。
	if s.probeReporter != nil {
		s.probeReporter(ctx, posID)
	}
	log.Printf("[probe] pos=%d expired-close done net=$%.4f exit=probe_expired", posID, net)
	return nil
}

// probeFeesUSD 平仓费用聚合（feesUSD 列口径=Σ 腿实采 fee_usd；MT 腿 fee NULL 按
// venue 不可信源计 0=actual，CEX 腿 NULL→mixed）。
func (s *Service) probeFeesUSD(ctx context.Context, posID int64) (*float64, string) {
	legs, err := s.store.ListPositionLegs(ctx, posID)
	if err != nil {
		return nil, "none"
	}
	var sum float64
	filled, actual := 0, 0
	for _, l := range legs {
		if l.Status != "FILLED" {
			continue
		}
		filled++
		if l.FeeUSD != nil {
			sum += *l.FeeUSD
			actual++
		} else if keydomain.IsMTKey(l.Venue) {
			actual++ // MT 费不可信源——显式 0 计 actual（EnsureLegCommissionCashflow 同裁定）
		}
	}
	if filled == 0 || actual == 0 {
		return nil, "none"
	}
	if actual < filled {
		return &sum, "mixed"
	}
	return &sum, "actual"
}

// revertProbeOpen 平仓失败回 OPEN（等下轮重试）+ dedup warn。
func (s *Service) revertProbeOpen(ctx context.Context, posID int64, why string) {
	if err := s.store.UpdatePositionStatus(ctx, posID, "OPEN"); err != nil {
		s.probeCritical(ctx, posID, fmt.Sprintf("平仓失败后回 OPEN 失败: %v（原由: %s）", err, why))
		return
	}
	msg := fmt.Sprintf("[probe] pos=%d 到期平仓未成，回 OPEN 待重试: %s", posID, why)
	if err := s.store.InsertAlertDedup(ctx, "warn", msg, probeCloseFailRetryWindow); err != nil {
		log.Printf("[probe] pos=%d retry alert: %v", posID, err)
	}
}
