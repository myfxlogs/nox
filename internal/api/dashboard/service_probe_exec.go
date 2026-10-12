// service_probe_exec.go —— D-047：OpenProbe 执行段（守卫过后才进此文件面）。
// 序：CreatePosition(OPENING) → bm.PlaceOrder → CreatePositionLeg(OPEN/FILLED) → OPEN。
// position 行先落=venue 侧实单与 DB 态之间永有审计锚（败/UNKNOWN 都留行）。
package dashboard

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"arb/internal/data/bus"
	"arb/internal/data/keydomain"
	"arb/internal/store/pgstore"
	"arb/internal/trading/exec"

	v1 "arb/internal/gen/dashboardv1"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// openProbeExecute 守卫全过后的执行段（q=开单价帧，notionalUSD 已算）。
func (s *Service) openProbeExecute(ctx context.Context, r *v1.OpenProbeRequest, env string, notionalUSD float64, q bus.Quote) (*connect.Response[v1.OpenProbeResponse], error) {
	note := r.GetPurpose()
	autoClose := r.GetAutoCloseAt().AsTime()
	decisionRef := r.GetDecisionRef()
	posID, err := s.store.CreatePosition(ctx, &pgstore.Position{
		OpportunityID: "PROBE:" + r.GetCanonical() + ":" + decisionRef,
		Type:          pgstore.PositionTypePROBE,
		Status:        "OPENING",
		Note:          note,
		NotionalUSD:   &notionalUSD,
		Environment:   env,
		DecisionRef:   &decisionRef,
		AutoCloseAt:   &autoClose,
	})
	if err != nil {
		return s.probeReject(ctx, r, fmt.Sprintf("position 落库失败: %v", err))
	}

	fill, err := s.bm.PlaceOrder(ctx, exec.Order{
		Symbol:        q.VenueSymbol,
		Side:          r.GetSide(),
		Qty:           r.GetQty(),
		Venue:         r.GetVenueKey(),
		PosID:         posID,
		ClientOrderID: fmt.Sprintf("probe:%d", posID),
	})
	if err != nil {
		// 受理未确认（ErrFillUnconfirmed：有 OrderID 未达终态）→ 留 OPENING + critical，
		// 不假写 FAILED（订单可能已成交——venue 实仓靠对账/人工收编）。
		var unconfirmed *exec.ErrFillUnconfirmed
		if errors.As(err, &unconfirmed) {
			s.probeCritical(ctx, posID, fmt.Sprintf("开单受理未确认 order=%s（venue 仓状态待对账）: %v", unconfirmed.OrderID, err))
			return connect.NewResponse(&v1.OpenProbeResponse{
				PositionId: posID, Status: "UNCONFIRMED", // R1⑤：未确认态明示，不假写 FAILED
				Message: fmt.Sprintf("受理未确认（仓 #%d 留 OPENING 待对账）: %v", posID, err)}), nil
		}
		if uerr := s.store.UpdatePositionStatus(ctx, posID, "FAILED"); uerr != nil {
			log.Printf("[probe] pos=%d mark FAILED after place err: %v", posID, uerr)
		}
		return s.probeReject(ctx, r, fmt.Sprintf("下单失败（仓 #%d 已记 FAILED）: %v", posID, err))
	}
	if fill.Qty <= 0 {
		fill.Qty = r.GetQty() // venue 未回报量 → 按请求量记（成交价权威=fill.Price）
	}
	now := time.Now()
	feeUSD := fill.FeeUSD
	leg := &pgstore.PositionLeg{
		PositionID: posID, LegNo: 1, Phase: "OPEN",
		Venue: r.GetVenueKey(), Account: keydomain.BareOf(r.GetVenueKey()),
		Symbol: q.VenueSymbol, Side: r.GetSide(), Qty: r.GetQty(),
		PriceExpected: q.Mid(), PriceActual: &fill.Price,
		OrderID: fill.OrderID, Status: "FILLED",
		AttemptNo: 1, FilledQty: &fill.Qty, AvgFillPrice: &fill.Price,
		FeeUSD: &feeUSD, SubmittedAt: &now, Ts: now,
		ClientOrderID: fmt.Sprintf("probe:%d", posID),
	}
	if err := s.store.CreatePositionLeg(ctx, leg); err != nil {
		// venue 已成交但腿落库失败——实仓在而账无腿：critical + 留 OPENING 待人工/对账。
		s.probeCritical(ctx, posID, fmt.Sprintf("单腿已成交（order=%s px=%.6g）但 position_legs 落库失败: %v", fill.OrderID, fill.Price, err))
		return connect.NewResponse(&v1.OpenProbeResponse{
			PositionId: posID, Status: "FAILED",
			Message: "腿落库失败（venue 实仓存在，仓留 OPENING 待对账）"}), nil
	}
	if err := s.store.UpdatePositionStatus(ctx, posID, "OPEN"); err != nil {
		s.probeCritical(ctx, posID, fmt.Sprintf("腿已成交但 OPEN 标记失败: %v", err))
	}
	return connect.NewResponse(&v1.OpenProbeResponse{PositionId: posID, Status: "OPEN"}), nil
}

// probeCritical 探针关键告警（venue/DB 不一致态——比守卫拒绝更严重级）。
func (s *Service) probeCritical(ctx context.Context, posID int64, msg string) {
	if err := s.store.InsertAlert(ctx, nil, "critical", fmt.Sprintf("[probe] pos=%d %s", posID, msg)); err != nil {
		log.Printf("[probe] critical alert insert failed pos=%d: %v", posID, err)
	}
}

// ListProbes D-047①：探针仓读面（open_only=只在途）。
func (s *Service) ListProbes(ctx context.Context, req *connect.Request[v1.ListProbesRequest]) (*connect.Response[v1.ListProbesResponse], error) {
	rows, err := s.store.ListProbes(ctx, req.Msg.GetOpenOnly())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	out := &v1.ListProbesResponse{Probes: make([]*v1.ProbePosition, 0, len(rows))}
	for i := range rows {
		p := probeRowToProto(&rows[i])
		out.Probes = append(out.Probes, p)
	}
	return connect.NewResponse(out), nil
}

func probeRowToProto(r *pgstore.ProbeRow) *v1.ProbePosition {
	pb := &v1.ProbePosition{
		PositionId:  r.ID,
		VenueKey:    r.VenueKey,
		Canonical:   r.OpportunityID,
		Side:        r.Side,
		Qty:         r.LegQty,
		Note:        r.Note,
		Status:      r.Status,
		ExitKind:    r.ExitKind,
		NotionalUsd: r.NotionalUSD,
	}
	// canonical 从腿 symbol 反解不可靠——用 opportunity_id 段（"PROBE:<canonical>:<ref>"）。
	if parts := splitOppID(r.OpportunityID); parts != "" {
		pb.Canonical = parts
	}
	if r.DecisionRef != nil {
		pb.DecisionRef = *r.DecisionRef
	}
	if r.AutoCloseAt != nil {
		pb.AutoCloseAt = timestamppb.New(*r.AutoCloseAt)
	}
	if r.OpenedAt != nil {
		pb.OpenedAt = timestamppb.New(*r.OpenedAt)
	}
	if r.ClosedAt != nil {
		pb.ClosedAt = timestamppb.New(*r.ClosedAt)
	}
	return pb
}

// splitOppID 取 "PROBE:<canonical>:<ref>" 的 canonical 段。
func splitOppID(oppID string) string {
	rest := strings.TrimPrefix(oppID, "PROBE:")
	if rest == oppID {
		return ""
	}
	if i := strings.LastIndex(rest, ":"); i > 0 {
		return rest[:i]
	}
	return rest
}

// ListProbeProposals D-176①：探针提案待审读面（scan_kind='probe'）。
// 默认仅 proposed（待审卡）；include_decided=全态（审计对拍）。
func (s *Service) ListProbeProposals(ctx context.Context, req *connect.Request[v1.ListProbeProposalsRequest]) (*connect.Response[v1.ListProbeProposalsResponse], error) {
	status := "proposed"
	if req.Msg.GetIncludeDecided() {
		status = ""
	}
	var rows []*pgstore.ProbeProposal
	var err error
	if status == "" {
		for _, st := range []string{"proposed", "approved", "rejected", "running", "closed"} {
			part, perr := s.store.ExplorerProbeProposalList(ctx, st)
			if perr != nil {
				err = perr
				break
			}
			rows = append(rows, part...)
		}
	} else {
		rows, err = s.store.ExplorerProbeProposalList(ctx, status)
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	out := &v1.ListProbeProposalsResponse{Proposals: make([]*v1.ProbeProposalCard, 0, len(rows))}
	for _, p := range rows {
		c := p.Contract
		out.Proposals = append(out.Proposals, &v1.ProbeProposalCard{
			ProposalId:      p.ID,
			Purpose:         c.Purpose,
			Falsify:         c.Falsify,
			VenueKey:        c.VenueKey,
			Canonical:       c.Canonical,
			Side:            c.Side,
			Qty:             c.Qty,
			NotionalUsd:     c.NotionalUSD,
			ExpectedCostUsd: c.ExpectedCostUSD,
			AutoCloseAt:     timestamppb.New(c.AutoCloseAt),
			Basis:           c.Basis,
			GateNote:        p.GateNote,
			ProposedAt:      timestamppb.New(p.ScanDate),
		})
	}
	return connect.NewResponse(out), nil
}
