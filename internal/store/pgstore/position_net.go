// position_net.go —— D-013 账本单真相。
// ① exit_kind 权威全集常量（写入点直写，不再 ExitKindFromReason 字符串反推新值）。
// ② PositionNetUSD：仓净额唯一合成式 = Σ腿价差盈亏 + Σcashflows(funding,swap)
//   - Σcashflows(commission)——commission 行入库即负值（成本），加和=扣减；
//     positions.realized_pnl 与 position_settlements.net_realized_usd 只从它写。
//
// ③ EnsureLegCommissionCashflow：每条 FILLED/EXTERNAL 腿必有 commission 行
//
//	（event_key=leg:<id>），CEX 腿=-fee_usd 实采、MT 腿显式 0 行；
//	EXTERNAL/CEX 腿 fee NULL → 结算 completeness=partial 不入样（settlement 侧判）。
package pgstore

import (
	"context"
	"fmt"
	"time"

	"arb/internal/keydomain"
	"arb/internal/notional"
)

// exit_kind 权威全集（D-013 ③）：写入点直写常量，禁字符串反推。
const (
	ExitKindHarvest       = "harvest"
	ExitKindStop          = "stop"
	ExitKindExecDrift     = "exec_drift"
	ExitKindCarryDeath    = "carry_death"
	ExitKindDisaster      = "disaster"
	ExitKindRotate        = "rotate"
	ExitKindManual        = "manual"
	ExitKindOther         = "other"
	ExitKindLeggedUnwind  = "legged_unwind"  // 从未双腿成交，unwind 已成腿（#507 形态）
	ExitKindIncidentClose = "incident_close" // await_session/不可行动腿或长时残腿告警平仓（#502/#505 形态）
)

// incidentPositionPredicate 事故族排除谓词（D-013 ③）：聚合 SQL 拼接点统一出处。
// 事故族 = exit_kind∈{legged_unwind, incident_close} ∪ incident_attributed
// ∪ status∈(LEGGED,FAILED)。JOIN 别名约定 p=positions、s=position_settlements。
const incidentPositionPredicate = `(COALESCE(p.exit_kind,'') IN ('legged_unwind','incident_close') OR COALESCE(s.incident_attributed,false) OR p.status IN ('LEGGED','FAILED'))`

// PositionLegPricePnlUSD 仓腿价差盈亏组件（D-013 单源的一半）。
// 口径 = exec.legPricePnL 同式：对每个 OPEN FILLED 腿取其全部 CLOSE FILLED/EXTERNAL 腿，
// (close_px−open_px)×qty×cs×dir×fx 逐腿加总；qty=close 腿 filled_qty（缺→OPEN 腿
// 成交口径）；cs：MT 腿(venue_type mt4/mt5)查 broker_symbol_map，CEX 腿=1；
// fx=canonical 计价币→USD。腿价/成交量缺失→该腿计 0（宁缺勿虚增，结算侧 completeness 判）。
// cs/fx 查不到 → error（fail-closed，不猜换算）。
func (s *Store) PositionLegPricePnlUSD(ctx context.Context, posID int64) (float64, error) {
	pos, err := s.GetPosition(ctx, posID)
	if err != nil {
		return 0, fmt.Errorf("get position %d: %w", posID, err)
	}
	if pos == nil {
		return 0, fmt.Errorf("position %d not found", posID)
	}
	legs, err := s.ListPositionLegs(ctx, posID)
	if err != nil {
		return 0, fmt.Errorf("list legs pos=%d: %w", posID, err)
	}
	fx := 1.0
	if q := notional.QuoteCcy(canonicalFromOppIDLocal(pos.OpportunityID)); q != "" && q != "USD" {
		rate, ok := s.USDPer(ctx, q, time.Now())
		if !ok || rate <= 0 {
			return 0, fmt.Errorf("pos=%d quote ccy %s fx unavailable", posID, q)
		}
		fx = rate
	}
	closes := map[int16][]PositionLeg{}
	for _, l := range legs {
		if l.Phase == "CLOSE" && (l.Status == "FILLED" || l.Status == "EXTERNAL") {
			closes[l.LegNo] = append(closes[l.LegNo], l)
		}
	}
	var total float64
	for _, leg := range legs {
		if leg.Phase != "OPEN" || leg.Status != "FILLED" || leg.PriceActual == nil {
			continue
		}
		dir := 1.0
		if leg.Side == "SELL" {
			dir = -1.0
		}
		for _, cl := range closes[leg.LegNo] {
			if cl.PriceActual == nil || *cl.PriceActual <= 0 {
				continue
			}
			qty := leg.EffectiveQty()
			if cl.FilledQty != nil && *cl.FilledQty > 0 {
				qty = *cl.FilledQty
			}
			cs, err := s.legContractSize(ctx, leg)
			if err != nil {
				return 0, err
			}
			total += (*cl.PriceActual - *leg.PriceActual) * qty * cs * dir * fx
		}
	}
	return total, nil
}

// legContractSize MT 腿查 broker_symbol_map（venue_key 复合键→broker_symbol），
// CEX 腿=1。MT 查无 → error（fail-closed，不猜换算）。
func (s *Store) legContractSize(ctx context.Context, leg PositionLeg) (float64, error) {
	if !keydomain.IsMTKey(leg.Venue) {
		return 1.0, nil
	}
	cs, ok, err := s.ContractSizeByBrokerSymbol(ctx, leg.Venue, leg.Symbol)
	if err != nil {
		return 0, fmt.Errorf("cs lookup %s %s: %w", leg.Venue, leg.Symbol, err)
	}
	if !ok || cs <= 0 {
		return 0, fmt.Errorf("cs missing for MT leg %s %s（宁缺勿猜）", leg.Venue, leg.Symbol)
	}
	return cs, nil
}

// PositionNetUSD 仓净额唯一合成式（D-013 ②）。
// = 价差组件 + Σ(kind='funding'|'swap') + Σcommission。
// 价差组件：pricePnlOverride 非 nil 用之（exec 侧已含校准器权威值替换的腿价差）；
// nil → PositionLegPricePnlUSD 从落库腿重算（settlement/迁移口径）。
// commission 去重：腿键行（event_key='leg:%'）存在则只计之（新单一事实源）；
// 否则回落全部 commission 行（leg: 行未铺到的 legacy 仓——旧 exec:* 键是唯一记录）。
// 幂等：纯查询，调用方在终态写点使用。
func (s *Store) PositionNetUSD(ctx context.Context, posID int64, pricePnlOverride *float64) (float64, error) {
	var pricePnl float64
	if pricePnlOverride != nil {
		pricePnl = *pricePnlOverride
	} else {
		var err error
		pricePnl, err = s.PositionLegPricePnlUSD(ctx, posID)
		if err != nil {
			return 0, err
		}
	}
	var nonComm float64
	if err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount_usd),0) FROM position_cashflows
		 WHERE position_id=$1 AND kind IN ('funding','swap')`, posID).Scan(&nonComm); err != nil {
		return 0, fmt.Errorf("pos=%d funding/swap cashflows: %w", posID, err)
	}
	comm, err := s.commissionCashflowsUSD(ctx, posID)
	if err != nil {
		return 0, err
	}
	return pricePnl + nonComm + comm, nil
}

// commissionCashflowsUSD D-014：commission 净额（负值）单源——PositionNetUSD 与
// econ.EntryCostUSD 共用去重口径（腿键行存在则只计腿键，否则回落 legacy 全行）。
func (s *Store) commissionCashflowsUSD(ctx context.Context, posID int64) (float64, error) {
	var hasLegKeyed bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM position_cashflows
		 WHERE position_id=$1 AND kind='commission' AND event_key LIKE 'leg:%%')`, posID).Scan(&hasLegKeyed); err != nil {
		return 0, fmt.Errorf("pos=%d commission key check: %w", posID, err)
	}
	commQ := `SELECT COALESCE(SUM(amount_usd),0) FROM position_cashflows
		 WHERE position_id=$1 AND kind='commission'`
	if hasLegKeyed {
		commQ += ` AND event_key LIKE 'leg:%'`
	}
	var comm float64
	if err := s.pool.QueryRow(ctx, commQ, posID).Scan(&comm); err != nil {
		return 0, fmt.Errorf("pos=%d commission cashflows: %w", posID, err)
	}
	return comm, nil
}

// CommissionPaidUSD D-014：本仓已付手续费（正值=|Σcommission cashflows|），econ.EntryCostUSD 数据源。
// hasRows=本仓存在任一 commission 行（区分「真零已付」与「D-013 前 legacy 无行」）。
func (s *Store) CommissionPaidUSD(ctx context.Context, posID int64) (float64, bool, error) {
	var n int
	if err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM position_cashflows WHERE position_id=$1 AND kind='commission'`, posID).Scan(&n); err != nil {
		return 0, false, fmt.Errorf("pos=%d commission count: %w", posID, err)
	}
	comm, err := s.commissionCashflowsUSD(ctx, posID)
	return -comm, n > 0, err
}

// EnsureLegCommissionCashflow D-013 ①：每条 FILLED/EXTERNAL 腿必有 commission 行。
// event_key='leg:'+position_legs.id（腿行主键——腿级唯一、天然幂等，D-422 R1 同思路）。
// 金额：amountUSD 显式传入（历史回填等调用方已算好的 USD 成本，入库为负值=传入正值取负）；
// nil → 按腿派生：MT 腿(venue_type mt4/mt5)显式 0（MT 费不可信源，D-014 幻影费实证）、
// CEX 腿=-COALESCE(fee_usd,0)（fee NULL → 显式 0 行——结算侧 fee NULL → partial，
// 行在值空两事实分开记）。非终态腿（PENDING/SUBMITTED/FAILED）不写。
// 幂等：event_key ON CONFLICT DO NOTHING；查询/写入失败 fail-closed 返回 error。
func (s *Store) EnsureLegCommissionCashflow(ctx context.Context, leg PositionLeg, amountUSD *float64) error {
	if leg.Status != "FILLED" && leg.Status != "EXTERNAL" {
		return nil
	}
	if leg.ID == 0 {
		return fmt.Errorf("pos=%d leg=%d %s: leg row id missing", leg.PositionID, leg.LegNo, leg.Phase)
	}
	amount := 0.0
	isMT := keydomain.IsMTKey(leg.Venue)
	switch {
	case isMT:
		// MT 腿恒显式 0（费不可信源——即便调用方传值也按裁定写 0）
	case amountUSD != nil:
		amount = -*amountUSD
	case leg.FeeUSD != nil:
		amount = -*leg.FeeUSD
	}
	now := time.Now().UTC()
	eventKey := fmt.Sprintf("leg:%d", leg.ID)
	quality := "actual"
	src := "exec"
	cf := &Cashflow{
		PositionID: leg.PositionID, LegNo: &leg.LegNo, Kind: "commission",
		Venue: leg.Venue, AmountUSD: amount, Ts: now, EventKey: &eventKey,
		Source: &src, Quality: &quality, OccurredAt: &now, ReceivedTs: &now,
		AccountID: venueAccountID(leg.Venue),
	}
	if _, err := s.InsertCashflowFull(ctx, cf); err != nil {
		return fmt.Errorf("leg commission cashflow pos=%d leg=%d id=%d: %w",
			leg.PositionID, leg.LegNo, leg.ID, err)
	}
	return nil
}

// venueAccountID venue 键前缀数字 → account_id（swapledger/Exec 同款，pgstore 本地副本）。
func venueAccountID(venue string) *int64 {
	k := keydomain.Parse(venue)
	if k.Type == "" {
		return nil
	}
	var id int64
	if _, err := fmt.Sscanf(k.Venue, "%d", &id); err != nil {
		return nil
	}
	return &id
}

// SetIncidentExitKind D-013 ③：事故 exit_kind 权威覆盖写（不受 UpdatePositionExitKind
// IS NULL 首因限制——legged_unwind/incident_close 是事后归因终态事实，优先于触发侧
// 策略 kind）。仅接受两事故 kind 参数，调用点=exec 平仓终态。
func (s *Store) SetIncidentExitKind(ctx context.Context, posID int64, kind string) error {
	if kind != ExitKindLeggedUnwind && kind != ExitKindIncidentClose {
		return fmt.Errorf("kind %q 非事故集（legged_unwind/incident_close 专用写口）", kind)
	}
	_, err := s.pool.Exec(ctx, `UPDATE positions SET exit_kind=$1 WHERE id=$2`, kind, posID)
	return err
}
