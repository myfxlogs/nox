// D-272 逆回购操作记录+策略验证闭环 Dashboard RPC handlers（S2）。
// 5 RPC：RecordRepoTrade / ListRepoTrades / SettleRepoTrade / DeleteRepoTrade / GetRepoStrategyValidation。
// 后端计算（D-043 前端零信任）：理论快照抓取+偏差计算+策略验证聚合全在后端。
// 辅助函数（instrumentNames/computeDefaultMaturityDate/buildSignalSnapshot/isUniqueViolation）在 service_repo_trades_helpers.go。
package dashboard

import (
	"context"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"arb/internal/store/pgstore"
)

// RecordRepoTrade 录入逆回购操作（手动）。
// 后端校验+抓取理论快照+计算偏差+插入+回显。
func (s *Service) RecordRepoTrade(ctx context.Context, req *connect.Request[v1.RecordRepoTradeRequest]) (*connect.Response[v1.RecordRepoTradeResponse], error) {
	m := req.Msg
	resp := &v1.RecordRepoTradeResponse{}

	// 1. 校验
	if !validInstrumentCodes[m.InstrumentCode] {
		resp.Error = "instrument_code 非法（支持 204001/204007/204014/204028）"
		return connect.NewResponse(resp), nil
	}
	tradeDate, err := time.Parse("2006-01-02", m.TradeDate)
	if err != nil {
		resp.Error = fmt.Sprintf("trade_date 格式应为 YYYY-MM-DD: %v", err)
		return connect.NewResponse(resp), nil
	}
	if m.HoldingDays <= 0 {
		resp.Error = "holding_days 必须 > 0"
		return connect.NewResponse(resp), nil
	}
	if m.PrincipalCny <= 0 {
		resp.Error = "principal_cny 必须 > 0"
		return connect.NewResponse(resp), nil
	}
	if m.ActualRatePct <= 0 {
		resp.Error = "actual_rate_pct 必须 > 0"
		return connect.NewResponse(resp), nil
	}

	// 2. 计算 maturity_date（§0 A-1 修订：用交易日历前扫）
	var maturityDate time.Time
	if m.MaturityDate != nil && *m.MaturityDate != "" {
		md, err := time.Parse("2006-01-02", *m.MaturityDate)
		if err != nil {
			resp.Error = fmt.Sprintf("maturity_date 格式应为 YYYY-MM-DD: %v", err)
			return connect.NewResponse(resp), nil
		}
		maturityDate = md
		// §0 L1：校验下界 maturity_date >= trade_date
		if maturityDate.Before(tradeDate) {
			resp.Error = "maturity_date 不能早于 trade_date"
			return connect.NewResponse(resp), nil
		}
	} else {
		maturityDate = computeDefaultMaturityDate(ctx, s.store, tradeDate)
	}

	// 3. 抓取理论快照
	rates, err := s.store.LatestRepoRates(ctx)
	if err != nil {
		return nil, fmt.Errorf("latest repo rates: %w", err)
	}
	var theoreticalRatePct *float64
	for _, r := range rates {
		if r.Code == m.InstrumentCode {
			v := r.RatePct
			theoreticalRatePct = &v
			break
		}
	}
	avg30d, avgHas, err := s.store.RepoRate30dAvg(ctx, m.InstrumentCode)
	if err != nil {
		return nil, fmt.Errorf("repo rate 30d avg: %w", err)
	}
	cfg, err := s.store.GetRepoConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("get repo config: %w", err)
	}
	idleCny := cfg.IdleCny

	// 4. 计算理论收益（利率非空时）
	var theoreticalReturnByIdle, theoreticalReturnCny *float64
	var deviationRateBps *float64
	if theoreticalRatePct != nil {
		byIdle := *theoreticalRatePct / 100 * idleCny * float64(m.HoldingDays) / 365
		byPrincipal := *theoreticalRatePct / 100 * m.PrincipalCny * float64(m.HoldingDays) / 365
		theoreticalReturnByIdle = &byIdle
		theoreticalReturnCny = &byPrincipal
		bps := (m.ActualRatePct - *theoreticalRatePct) * 100
		deviationRateBps = &bps
	}

	// 5. 计算录入时偏差
	deviationPrincipalCny := m.PrincipalCny - idleCny

	// 5.1 D-276 计算手续费（成交金额 × 品种佣金率）
	feeCny := m.PrincipalCny * feeRates[m.InstrumentCode]

	// 6. 生成 signal_snapshot JSON
	snap := buildSignalSnapshot(m.InstrumentCode, theoreticalRatePct, avg30d, avgHas, idleCny, theoreticalReturnByIdle)
	snapStr := string(snap)

	// 7. 构造行并插入
	row := &pgstore.RepoTradeRow{
		InstrumentCode:          m.InstrumentCode,
		TradeDate:               tradeDate,
		MaturityDate:            maturityDate,
		HoldingDays:             int(m.HoldingDays),
		PrincipalCny:            m.PrincipalCny,
		ActualRatePct:           m.ActualRatePct,
		TheoreticalRatePct:      theoreticalRatePct,
		TheoreticalReturnByIdle: theoreticalReturnByIdle,
		TheoreticalReturnCny:    theoreticalReturnCny,
		IdleCnyAtRecord:         &idleCny,
		SignalSnapshot:          &snapStr,
		DeviationRateBps:        deviationRateBps,
		DeviationPrincipalCny:   &deviationPrincipalCny,
		FeeCny:                  &feeCny,
	}
	if m.RepoOpId != nil {
		row.RepoOpID = m.RepoOpId
	}
	rationale := strings.TrimSpace(m.Rationale)
	if rationale != "" {
		row.Rationale = &rationale
	}

	id, err := s.store.InsertRepoTrade(ctx, row)
	if err != nil {
		// 唯一约束冲突（trade_date + instrument_code 重复）
		if isUniqueViolation(err) {
			resp.Error = "该日期已录入该品种，请先删除旧记录再重新录入"
			return connect.NewResponse(resp), nil
		}
		return nil, fmt.Errorf("insert repo trade: %w", err)
	}

	// 8. 录入后 info 级告警落库（设计 §8）
	instrumentName := instrumentNames[m.InstrumentCode]
	var theoreticalReturnForAlert float64
	if theoreticalReturnCny != nil {
		theoreticalReturnForAlert = *theoreticalReturnCny
	}
	alertMsg := fmt.Sprintf("[逆回购记录] %s ¥%.0f @%.2f%% 到期 %s 理论收益 ¥%.2f",
		instrumentName, m.PrincipalCny, m.ActualRatePct,
		maturityDate.Format("2006-01-02"), theoreticalReturnForAlert)
	_ = s.store.InsertAlert(ctx, nil, "info", alertMsg)

	// 9. 回显
	resp.Ok = true
	resp.Id = id
	resp.MaturityDate = maturityDate.Format("2006-01-02")
	resp.DeviationPrincipalCny = deviationPrincipalCny
	if theoreticalRatePct != nil {
		resp.TheoreticalRatePct = theoreticalRatePct
	}
	if theoreticalReturnByIdle != nil {
		resp.TheoreticalReturnByIdle = theoreticalReturnByIdle
	}
	if theoreticalReturnCny != nil {
		resp.TheoreticalReturnCny = theoreticalReturnCny
	}
	if deviationRateBps != nil {
		resp.DeviationRateBps = deviationRateBps
	}
	// D-276 回显手续费+净理论收益
	resp.FeeCny = feeCny
	if theoreticalReturnCny != nil {
		netTheo := *theoreticalReturnCny - feeCny
		resp.NetTheoreticalReturnCny = &netTheo
	}
	return connect.NewResponse(resp), nil
}

// SettleRepoTrade 结算确认（到期后确认实际收益）。
func (s *Service) SettleRepoTrade(ctx context.Context, req *connect.Request[v1.SettleRepoTradeRequest]) (*connect.Response[v1.SettleRepoTradeResponse], error) {
	m := req.Msg
	resp := &v1.SettleRepoTradeResponse{}

	if m.Id <= 0 {
		resp.Error = "id 必须 > 0"
		return connect.NewResponse(resp), nil
	}
	if m.ActualReturnCny < 0 {
		resp.Error = "actual_return_cny 必须 >= 0"
		return connect.NewResponse(resp), nil
	}

	trade, err := s.store.GetRepoTrade(ctx, m.Id)
	if err != nil {
		return nil, fmt.Errorf("get repo trade: %w", err)
	}
	if trade == nil {
		resp.Error = "记录不存在"
		return connect.NewResponse(resp), nil
	}
	if trade.Status != "open" {
		resp.Error = "该记录已结算"
		return connect.NewResponse(resp), nil
	}

	// 计算收益偏差（D-276：vs 净理论 = theoretical_return_cny − fee_cny）
	var deviationReturnCny, deviationReturnPct *float64
	if trade.TheoreticalReturnCny != nil {
		netTheo := *trade.TheoreticalReturnCny
		if trade.FeeCny != nil {
			netTheo -= *trade.FeeCny
		}
		cny := m.ActualReturnCny - netTheo
		deviationReturnCny = &cny
		if netTheo == 0 {
			zero := 0.0
			deviationReturnPct = &zero
		} else {
			pct := cny / netTheo * 100
			deviationReturnPct = &pct
		}
	}

	if err := s.store.SettleRepoTrade(ctx, m.Id, m.ActualReturnCny, deviationReturnCny, deviationReturnPct); err != nil {
		return nil, fmt.Errorf("settle repo trade: %w", err)
	}

	resp.Ok = true
	if deviationReturnCny != nil {
		resp.DeviationReturnCny = deviationReturnCny
	}
	if deviationReturnPct != nil {
		resp.DeviationReturnPct = deviationReturnPct
	}
	return connect.NewResponse(resp), nil
}

// DeleteRepoTrade 删除未结算记录（录错更正路径，仅 open 态可删）。
func (s *Service) DeleteRepoTrade(ctx context.Context, req *connect.Request[v1.DeleteRepoTradeRequest]) (*connect.Response[v1.DeleteRepoTradeResponse], error) {
	m := req.Msg
	resp := &v1.DeleteRepoTradeResponse{}

	if m.Id <= 0 {
		resp.Error = "id 必须 > 0"
		return connect.NewResponse(resp), nil
	}

	trade, err := s.store.GetRepoTrade(ctx, m.Id)
	if err != nil {
		return nil, fmt.Errorf("get repo trade: %w", err)
	}
	if trade == nil {
		resp.Error = "记录不存在"
		return connect.NewResponse(resp), nil
	}
	if trade.Status != "open" {
		resp.Error = "已结算记录不可删除"
		return connect.NewResponse(resp), nil
	}

	deleted, err := s.store.DeleteRepoTrade(ctx, m.Id)
	if err != nil {
		return nil, fmt.Errorf("delete repo trade: %w", err)
	}
	if !deleted {
		resp.Error = "删除失败（记录可能已被其他操作变更）"
		return connect.NewResponse(resp), nil
	}
	resp.Ok = true
	return connect.NewResponse(resp), nil
}
