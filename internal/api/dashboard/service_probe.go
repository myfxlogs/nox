// service_probe.go —— D-047：系统探针仓 PROBE 单腿路径。
// OpenProbe：守卫链（任一不过→拒并落 alerts warn）→ bm.PlaceOrder 开单腿 →
// positions(type='PROBE')+position_legs 落账。
// 守卫序：输入形参 → venue 白名单 → 控制面（KillSwitch/mode）→ 事故闸 →
// 报价+名义（≤ NOX_PROBE_MAX_NOTIONAL）→ 在途 ≤2 → 权益纪律。全 fail-closed。
// ExpireDueProbes：posMon 唤醒入口（定时器仅唤醒不携数据，决策查 DB 实时态）。
package dashboard

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"time"

	"nox/internal/data/keydomain"
	"nox/internal/platform/notional"
	"nox/internal/store/pgstore"

	v1 "nox/internal/gen/dashboardv1"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// probeMaxInFlight 在途 PROBE 上限（D-047② 硬编码业务常量，非配置项）。
const probeMaxInFlight = 2

// probeDecisionRefRe D-047 R1③：decision_ref 须为决策席 D# 形（D-047/D-048-R1 类）。
var probeDecisionRefRe = regexp.MustCompile(`^D-\d{3,}$`)

// probeEquitySnapshotMaxAge 权益快照新鲜度——同 autotrade.risk_lines.go
// riskEquitySnapshotMaxAge（5min tickFresh 口径；dashboard 不引 autotrade 防环依赖，
// 值对齐注记保留双写锚）。
const probeEquitySnapshotMaxAge = 5 * time.Minute

// probeEquityTypes 权益纪律「该 venue 在仓名义」计数域——同 autotrade.risk_lines.go
// riskEquityTypes 单源口径（XASSET_CARRY/FUNDING_HEDGE；PROBE 自身不入计数域——
// 单腿 ≤ 上限的小额验证仓，按 D-030 硬线只增本次腿名义）。
var probeEquityTypes = []string{"XASSET_CARRY", "FUNDING_HEDGE"}

// annotateProbeMeta D-047⑤：关联仓为 type='PROBE' 的 broker 持仓标注
// probe_decision_ref + probe_auto_close_at（前端「探针·D#」标+到期时刻数据源）。
// 查询失败不阻断（fail-open，probe 字段留空=非探针显示）。
func annotateProbeMeta(ctx context.Context, store *pgstore.Store, bps []*v1.BrokerPosition) {
	if len(bps) == 0 || store == nil {
		return
	}
	var ids []int64
	seen := map[int64]bool{}
	for _, bp := range bps {
		if bp.PositionId != 0 && !seen[bp.PositionId] {
			seen[bp.PositionId] = true
			ids = append(ids, bp.PositionId)
		}
	}
	if len(ids) == 0 {
		return
	}
	meta, err := store.ProbeMetaByPositionIDs(ctx, ids)
	if err != nil {
		return
	}
	for _, bp := range bps {
		m, ok := meta[bp.PositionId]
		if !ok {
			continue
		}
		bp.ProbeDecisionRef = m.DecisionRef
		if m.AutoCloseAt != nil {
			bp.ProbeAutoCloseAt = timestamppb.New(*m.AutoCloseAt)
		}
	}
}

// probeReject 守卫拒绝统一面：warn 告警落库 + FAILED 响应（非 connect error——
// 业务拒绝属正常返回，告警即审计痕迹）。
func (s *Service) probeReject(ctx context.Context, req *v1.OpenProbeRequest, reason string) (*connect.Response[v1.OpenProbeResponse], error) {
	msg := fmt.Sprintf("[probe] reject %s %s %s qty=%.4g ref=%s: %s",
		req.GetVenueKey(), req.GetCanonical(), req.GetSide(), req.GetQty(), req.GetDecisionRef(), reason)
	if err := s.store.InsertAlert(ctx, nil, "warn", msg); err != nil {
		log.Printf("[probe] alert insert failed: %v (reject reason: %s)", err, reason)
	}
	return connect.NewResponse(&v1.OpenProbeResponse{Status: "FAILED", Message: reason}), nil
}

// OpenProbe D-047①②：开系统探针仓单腿仓。
// [exec-entry:committee-exempt] D-262 M4.3③：探针=委员会调查件，自有守卫链
// （RPC 开关/venue 白名单/控制面/事故闸/在途上限/权益纪律），拒单全量落 alerts warn。
// 不进 Gate.Submit——探针腿异构（单腿调查仓，非套利对），明示例外。
func (s *Service) OpenProbe(ctx context.Context, req *connect.Request[v1.OpenProbeRequest]) (*connect.Response[v1.OpenProbeResponse], error) {
	r := req.Msg
	if s.store == nil || s.bm == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("probe path not wired"))
	}
	if r.GetVenueKey() == "" || r.GetCanonical() == "" || r.GetDecisionRef() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("venue_key/canonical/decision_ref 必填"))
	}
	if !probeDecisionRefRe.MatchString(r.GetDecisionRef()) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("decision_ref 须为 D# 形（^D-\\d{3,}$）"))
	}
	if r.GetSide() != "BUY" && r.GetSide() != "SELL" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("side 须为 BUY|SELL"))
	}
	if r.GetQty() <= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("qty 须 >0"))
	}
	if r.GetAutoCloseAt() == nil || !r.GetAutoCloseAt().AsTime().After(time.Now()) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("auto_close_at 必填且须在未来"))
	}
	if s.acv.running == nil {
		return s.probeReject(ctx, r, "config view 未装配（fail-closed）")
	}
	cfg := s.acv.running
	// 守卫⓪（R1② 第一道）：探针 RPC 总开关（acv 热读 running 视图；缺省 false）。
	if !cfg.ProbeRPCEnabled {
		return s.probeReject(ctx, r, "探针通道未启用（NOX_PROBE_RPC_ENABLED=false）")
	}

	// 守卫①：venue 须在 autotrade_allow_rules 启用集（kind='venue'，simulated=env demo）。
	venues, _, err := s.store.LoadAutotradeAllowRules(ctx, cfg.Environment == "demo")
	if err != nil {
		return s.probeReject(ctx, r, fmt.Sprintf("allow rules 查询失败: %v", err))
	}
	if !venues[r.GetVenueKey()] {
		return s.probeReject(ctx, r, fmt.Sprintf("venue %s 不在启用集", r.GetVenueKey()))
	}

	// 守卫②：控制面（KillSwitch/mode——与 ExecuteOpportunityInternal 同检查器）。
	if s.gateChecker == nil {
		return s.probeReject(ctx, r, "控制面未接线")
	}
	if mode, allowed, reason, gerr := s.gateChecker.CheckGate(ctx); gerr != nil || !allowed {
		return s.probeReject(ctx, r, fmt.Sprintf("控制面拒开（mode=%s reason=%s err=%v）", mode, reason, gerr))
	}

	// 守卫③：事故闸（D-032：∃ id>ack 游标事故仓 → 拒开；与 gate_incident 同谓词）。
	ackID := int64(cfg.RiskIncidentAckID)
	if v, ok, aerr := s.store.AppConfigInt64(ctx, "NOX_RISK_INCIDENT_ACK_ID"); aerr != nil {
		return s.probeReject(ctx, r, fmt.Sprintf("incident ack 读取失败: %v", aerr))
	} else if ok {
		ackID = v
	}
	blocker, berr := s.store.IncidentBlockerID(ctx, ackID)
	if berr != nil {
		return s.probeReject(ctx, r, fmt.Sprintf("事故闸查询失败: %v", berr))
	}
	if blocker > 0 {
		return s.probeReject(ctx, r, fmt.Sprintf("事故仓 #%d 未归档（ack=%d）", blocker, ackID))
	}

	// 守卫④：报价+名义。qbus 最新帧（venue 原生符号+中价）；缺/陈 → fail-closed。
	now := time.Now()
	q, ok := s.qbus.Latest(r.GetCanonical(), r.GetVenueKey())
	if !ok || q.Mid() <= 0 || q.VenueSymbol == "" {
		return s.probeReject(ctx, r, fmt.Sprintf("%s@%s 无报价/原生符号", r.GetCanonical(), r.GetVenueKey()))
	}
	if s.freshTTL > 0 && now.Sub(q.Time) > s.freshTTL {
		return s.probeReject(ctx, r, fmt.Sprintf("%s@%s 报价陈旧 %s", r.GetCanonical(), r.GetVenueKey(), now.Sub(q.Time).Round(time.Second)))
	}
	cs := 1.0
	if keydomain.IsMTKey(r.GetVenueKey()) {
		v, csOK, cerr := s.store.ContractSizeByBrokerSymbol(ctx, r.GetVenueKey(), q.VenueSymbol)
		if cerr != nil {
			return s.probeReject(ctx, r, fmt.Sprintf("contract_size 查询失败: %v", cerr))
		}
		if !csOK || v <= 0 {
			return s.probeReject(ctx, r, fmt.Sprintf("%s@%s contract_size 缺（宁缺勿猜）", q.VenueSymbol, r.GetVenueKey()))
		}
		cs = v
	}
	fx := 1.0
	if ccy := notional.QuoteCcy(r.GetCanonical()); ccy != "" && ccy != "USD" {
		rate, fxStale, fxOK := s.usdPerOrStale(ctx, ccy, "", now)
		_ = fxStale
		if !fxOK || rate <= 0 {
			return s.probeReject(ctx, r, fmt.Sprintf("%s 计价币 %s fx 不可得", r.GetCanonical(), ccy))
		}
		fx = rate
	}
	notionalUSD := notional.LegUSD(r.GetQty(), cs, q.Mid(), fx)

	// 守卫⑤：名义 ≤ NOX_PROBE_MAX_NOTIONAL（app_config 链，缺省 1000）。
	if maxN := cfg.ProbeMaxNotionalUSD; maxN > 0 && notionalUSD > maxN {
		return s.probeReject(ctx, r, fmt.Sprintf("名义 $%.2f > 上限 $%.2f", notionalUSD, maxN))
	}

	// 守卫⑥：在途 PROBE ≤2。
	active, cerr := s.store.CountActiveProbes(ctx)
	if cerr != nil {
		return s.probeReject(ctx, r, fmt.Sprintf("在途探针计数失败: %v", cerr))
	}
	if active >= probeMaxInFlight {
		return s.probeReject(ctx, r, fmt.Sprintf("在途探针 %d 已满上限 %d", active, probeMaxInFlight))
	}

	// 守卫⑦：权益纪律（D-032③ 同口径：venue 权益 ≥ pct%×(在仓名义+本次腿名义)）。
	pct := cfg.RiskCexEquityPct
	if keydomain.IsMTKey(r.GetVenueKey()) {
		pct = cfg.RiskCfdEquityPct
	}
	if pct > 0 {
		expo, xerr := s.store.VenueNotionalExposure(ctx, probeEquityTypes)
		if xerr != nil {
			return s.probeReject(ctx, r, fmt.Sprintf("venue 敞口查询失败: %v", xerr))
		}
		eq, _, asOf, eqOK := s.AccountEquityUSDAt(r.GetVenueKey())
		// D-178：缺/超龄先定向回读复判一次（同 idle_funds 同径）——静默 venue
		// asOf 冻结≠真不可达；仍缺/超龄才按原径拒。
		if !eqOK || now.Sub(asOf) > probeEquitySnapshotMaxAge {
			s.RefreshAccountEquity(ctx, r.GetVenueKey())
			eq, _, asOf, eqOK = s.AccountEquityUSDAt(r.GetVenueKey())
		}
		if !eqOK {
			return s.probeReject(ctx, r, fmt.Sprintf("%s 权益快照缺", r.GetVenueKey()))
		}
		if now.Sub(asOf) > probeEquitySnapshotMaxAge {
			return s.probeReject(ctx, r, fmt.Sprintf("%s 权益快照陈旧 %s", r.GetVenueKey(), now.Sub(asOf).Round(time.Second)))
		}
		if need := (expo[r.GetVenueKey()] + notionalUSD) * pct / 100; eq < need {
			return s.probeReject(ctx, r, fmt.Sprintf("权益 $%.0f < %.0f%%×(在仓$%.0f+本次$%.0f)=$%.0f", eq, pct, expo[r.GetVenueKey()], notionalUSD, need))
		}
	}
	return s.openProbeExecute(ctx, r, cfg.Environment, notionalUSD, q)
}
