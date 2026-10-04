package dashboard

import (
	"context"
	"fmt"
	"time"

	"arb/internal/bus"
	"arb/internal/store/pgstore"

	v1 "arb/internal/gen/dashboardv1"

	"connectrpc.com/connect"
)

func (s *Service) ListPositions(ctx context.Context, req *connect.Request[v1.ListPositionsRequest]) (*connect.Response[v1.ListPositionsResponse], error) {
	limit := int(req.Msg.GetLimit())
	offset := int(req.Msg.GetOffset())
	if limit <= 0 {
		limit = 50
	}
	pbPositions, total, err := s.assemblePositions(ctx, req.Msg.GetStatus(), limit, offset)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.ListPositionsResponse{Positions: pbPositions, Total: int32(total)}), nil
}

// assemblePositions D-571 包C：ListPositions 四段组装公共口（RPC 与 positionMon 发射器
// 同源复用——禁第二套组装）。返回 (pb 行集, total, err)；legs+settlement+enrich 全含。
func (s *Service) assemblePositions(ctx context.Context, status string, limit, offset int) ([]*v1.Position, int, error) {
	positions, err := s.store.ListPositions(ctx, status, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	// D-069：total 用于前端分页控件
	total, err := s.store.CountPositions(ctx, status)
	if err != nil {
		return nil, 0, err
	}
	var pbPositions []*v1.Position
	legsByPos := make(map[int64][]pgstore.PositionLeg, len(positions))
	posIDs := make([]int64, 0, len(positions))
	for _, p := range positions {
		pb := s.positionToProto(ctx, &p)
		// D-069：填充 legs（成交历史，OPEN + CLOSE phase）
		legs, err := s.store.ListPositionLegs(ctx, p.ID)
		if err != nil {
			return nil, 0, fmt.Errorf("list legs for position %d: %w", p.ID, err)
		}
		legsByPos[p.ID] = legs
		for _, l := range legs {
			pb.Legs = append(pb.Legs, positionLegToProto(&l))
		}
		pbPositions = append(pbPositions, pb)
		posIDs = append(posIDs, p.ID)
	}
	// D-106：批量 join 结算行填充 settled_realized_usd/settled_notional_usd/hold_days（单条 SQL IN，防 N+1）。
	if err := enrichSettlements(ctx, s.store, pbPositions, posIDs); err != nil {
		return nil, 0, err
	}
	// D-095：活跃行 enrich 7 派生字段（bpm 匹配+notional+应得+达成率+收敛型当前 bps）
	s.enrichPositions(ctx, pbPositions, legsByPos)
	return pbPositions, total, nil
}

// --- 个人持仓（财务线 TRX 持仓接入，D-046 + 实账化工作项 3） ---

// personalPositionToProto 组装单个个人持仓响应：取该 symbol 最新（Time 最新）且未超陈旧 TTL 的一帧 mid 为当前价，
// 复用 position.PersonalLevel 算告警态。无报价或全陈旧 → price 保持 0、level 空（诚实空态，G3）。
// 实账计算（D-043，全后端）：pnl/liq_price/距强平%/累计利息。
// D-208 v2：CMC 行（cmc_instrument_id 非空 + cmcSource 注入）走 ComputeCMCPositionPrice +
//
//	从 fin_cmc_positions 填 EntryPrice/Leverage/OpenedAt/Qty 真值（视图全自动）。
// positionPriceToProto 实时现价推送消息（D-049）：前端仅按 id 合并渲染，不计算。
// --- 每日收益（财务线工作项 3：个人持仓实账化） ---

// AddDailyIncome 录入一条每日收益记录。前端只传 day/energy/voting/interest/note；
// price/pnl_usd 后端自动取价与计算（D-043，前端不传价防伪造）；同 day 重复 → upsert 覆盖（支持纠错）。
func (s *Service) AddDailyIncome(ctx context.Context, req *connect.Request[v1.AddDailyIncomeRequest]) (*connect.Response[v1.AddDailyIncomeResponse], error) {
	m := req.Msg
	day, err := parseDay(m.GetDay())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid day %q: %w", m.GetDay(), err))
	}
	// 后端取实时价：TRXUSDT 最新未陈旧帧（复用陈旧守卫 G3）；无报价 → price/pnl 写 NULL（诚实空态）。
	var price *float64
	snap := s.qbus.Snapshot(ctx, []string{"TRXUSDT"})
	if q, ok := bus.LatestQuote(snap["TRXUSDT"], s.freshTTL, time.Now()); ok {
		p := q.Mid()
		price = &p
	}
	// pnl_usd = (entry − 现价) × qty：从 OPEN 的 short_hedge 行算（设计 §4；无报价不算）。
	var pnl *float64
	if price != nil {
		positions, err := s.store.ListPersonalPositions(ctx, "OPEN", 200, 0)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		for i := range positions {
			p := &positions[i]
			if p.Kind == "short_hedge" && p.EntryPrice != nil && p.Qty != nil {
				v := (*p.EntryPrice - *price) * *p.Qty
				pnl = &v
				break
			}
		}
	}
	id, err := s.store.AddDailyIncome(ctx, day, m.GetEnergyTrx(), m.GetVotingTrx(), m.GetInterestUsd(), price, pnl, m.GetNote())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	// §11 即时预警：查前日（to=day−1，limit 1 取最近前日）→ fundingWarning(本次, 前日)。
	// 前日查询失败按无前日降级（prev=nil 只走 R1）；预警是提示不是门禁，不阻塞提交。
	cur := m.GetInterestUsd()
	var prevPtr *float64
	prevDay := day.AddDate(0, 0, -1)
	if prevRows, err := s.store.ListDailyIncome(ctx, nil, &prevDay, 1, 0); err == nil && len(prevRows) > 0 {
		v := prevRows[0].InterestUSD
		prevPtr = &v
	}
	return connect.NewResponse(&v1.AddDailyIncomeResponse{Id: id, Warning: fundingWarning(&cur, prevPtr, s.fundingDropRatio)}), nil
}

// ListDailyIncome 按 day 倒序返回每日收益；from/to 可选区间；income_usd 后端算（D-043）。
func (s *Service) ListDailyIncome(ctx context.Context, req *connect.Request[v1.ListDailyIncomeRequest]) (*connect.Response[v1.ListDailyIncomeResponse], error) {
	m := req.Msg
	var from, to *time.Time
	if m.GetFrom() != "" {
		t, err := parseDay(m.GetFrom())
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid from %q: %w", m.GetFrom(), err))
		}
		from = &t
	}
	if m.GetTo() != "" {
		t, err := parseDay(m.GetTo())
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid to %q: %w", m.GetTo(), err))
		}
		to = &t
	}
	rows, err := s.store.ListDailyIncome(ctx, from, to, int(m.GetLimit()), int(m.GetOffset()))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	items := make([]*v1.PositionDailyIncome, 0, len(rows))
	for i := range rows {
		item := dailyIncomeToProto(&rows[i])
		// §11 每行预警：day 倒序相邻对算（rows[i] 的 prev = rows[i+1]，更旧行）。
		// 边界：返回集最旧一行（i=len-1）的 prev 在集外 → best-effort（prev=nil，只可能 R1）。
		var prevPtr *float64
		if i+1 < len(rows) {
			v := rows[i+1].InterestUSD
			prevPtr = &v
		}
		item.Warning = fundingWarning(&rows[i].InterestUSD, prevPtr, s.fundingDropRatio)
		items = append(items, item)
	}
	return connect.NewResponse(&v1.ListDailyIncomeResponse{Items: items}), nil
}

// dailyIncomeToProto 组装单条每日收益：income_usd = energy×price + voting×price + interest（price 无值按 0）。
func dailyIncomeToProto(d *pgstore.DailyIncome) *v1.PositionDailyIncome {
	pb := &v1.PositionDailyIncome{
		Id:          d.ID,
		Day:         d.Day.Format("2006-01-02"),
		EnergyTrx:   d.EnergyTrx,
		VotingTrx:   d.VotingTrx,
		InterestUsd: d.InterestUSD,
		Note:        d.Note,
	}
	price := 0.0
	if d.Price != nil {
		pb.Price = *d.Price
		price = *d.Price
	}
	if d.PnlUSD != nil {
		pb.PnlUsd = *d.PnlUSD
	}
	pb.IncomeUsd = d.EnergyTrx*price + d.VotingTrx*price + d.InterestUSD
	return pb
}

// parseDay 严格解析 YYYY-MM-DD。Go time.Parse 对 "01" 占位宽松（接受 2026-8-19），
// 这里 round-trip 校验拒绝非常规格式。
func parseDay(s string) (time.Time, error) {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, err
	}
	if t.Format("2006-01-02") != s {
		return time.Time{}, fmt.Errorf("day must be YYYY-MM-DD")
	}
	return t, nil
}
