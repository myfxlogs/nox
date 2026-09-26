// D-292 价格提醒规则前端管理 Dashboard RPC handlers（S3）。
// 4 RPC：ListPriceAlertRules / CreatePriceAlertRule / UpdatePriceAlertRule / DeletePriceAlertRule。
// 后端校验（D-043 前端零信任）：symbol 规范化/op 合法/threshold>0/level 合法/message 非空。
// source_matched 由 service 调 QuoteBus.AllLatest() 按 symbol 匹配填入（防 typo 建哑规则）。
// 引擎 30s refreshLoop 自动重载，增删改后无需通知引擎。
package dashboard

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"connectrpc.com/connect"

	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/store/pgstore"
)

// validPriceAlertOps 合法比较运算符白名单。
var validPriceAlertOps = map[string]bool{">=": true, "<=": true}

// validPriceAlertLevels 合法告警级别白名单。
var validPriceAlertLevels = map[string]bool{"info": true, "warn": true, "critical": true}

// validatePriceAlertInput 校验规则输入字段（Create/Update 共用），返回非空 error 文案时即拒绝。
// symbol 规范化：去空格+转大写+≤16 字符；threshold>0 且有限数；message 非空 ≤200 字符。
func validatePriceAlertInput(symbol, op, level, message string, threshold float64) (string, string, string, string, string) {
	sym := strings.ToUpper(strings.TrimSpace(symbol))
	if sym == "" {
		return "", "", "", "", "symbol 不能为空"
	}
	if len(sym) > 16 {
		return "", "", "", "", "symbol 长度不能超过 16 字符"
	}
	if !validPriceAlertOps[op] {
		return "", "", "", "", fmt.Sprintf("op 非法（仅支持 >= 或 <=），got %q", op)
	}
	if math.IsNaN(threshold) || math.IsInf(threshold, 0) {
		return "", "", "", "", "threshold 非法（NaN/Inf）"
	}
	if threshold <= 0 {
		return "", "", "", "", "threshold 必须 > 0"
	}
	if !validPriceAlertLevels[level] {
		return "", "", "", "", fmt.Sprintf("level 非法（仅支持 info/warn/critical），got %q", level)
	}
	msg := strings.TrimSpace(message)
	if msg == "" {
		return "", "", "", "", "message 不能为空"
	}
	if len(msg) > 200 {
		return "", "", "", "", "message 长度不能超过 200 字符"
	}
	return sym, op, level, msg, ""
}

// sourceMatched 查 QuoteBus.AllLatest() 判断 symbol 是否在已订阅报价集合中（防 typo 永不触发）。
func (s *Service) sourceMatched(symbol string) bool {
	if s.qbus == nil {
		return false
	}
	for _, q := range s.qbus.AllLatest() {
		if q.Symbol == symbol {
			return true
		}
	}
	return false
}

// priceAlertRowToProto 将 pgstore 行转为 proto Row，填 last_alert_at（RFC3339，空=从未）+ source_matched。
func (s *Service) priceAlertRowToProto(r *pgstore.PriceAlertAdminRow) *v1.PriceAlertRuleRow {
	row := &v1.PriceAlertRuleRow{
		Id:            r.ID,
		Symbol:        r.Symbol,
		Op:            r.Op,
		Threshold:     r.Threshold,
		Level:         r.Level,
		Message:       r.Message,
		Enabled:       r.Enabled,
		SourceMatched: s.sourceMatched(r.Symbol),
	}
	if r.LastAlertAt != nil {
		row.LastAlertAt = r.LastAlertAt.Format(time.RFC3339)
	}
	return row
}

// ListPriceAlertRules 列出全部规则（含 disabled）。
func (s *Service) ListPriceAlertRules(ctx context.Context, _ *connect.Request[v1.ListPriceAlertRulesRequest]) (*connect.Response[v1.ListPriceAlertRulesResponse], error) {
	rows, err := s.store.ListPriceAlertRules(ctx)
	if err != nil {
		return nil, fmt.Errorf("list price alert rules: %w", err)
	}
	resp := &v1.ListPriceAlertRulesResponse{}
	for i := range rows {
		resp.Rules = append(resp.Rules, s.priceAlertRowToProto(&rows[i]))
	}
	return connect.NewResponse(resp), nil
}

// CreatePriceAlertRule 新建规则（默认 enabled=true）。
func (s *Service) CreatePriceAlertRule(ctx context.Context, req *connect.Request[v1.CreatePriceAlertRuleRequest]) (*connect.Response[v1.CreatePriceAlertRuleResponse], error) {
	m := req.Msg
	sym, op, level, msg, errMsg := validatePriceAlertInput(m.Symbol, m.Op, m.Level, m.Message, m.Threshold)
	if errMsg != "" {
		return connect.NewResponse(&v1.CreatePriceAlertRuleResponse{Error: errMsg}), nil
	}
	r, err := s.store.CreatePriceAlertRule(ctx, sym, op, m.Threshold, level, msg)
	if err != nil {
		return nil, fmt.Errorf("create price alert rule: %w", err)
	}
	s.kickConfig() // D-469b S5
	return connect.NewResponse(&v1.CreatePriceAlertRuleResponse{
		Ok:   true,
		Rule: s.priceAlertRowToProto(r),
	}), nil
}

// UpdatePriceAlertRule 全字段更新（含 enabled 启停），按 id。
func (s *Service) UpdatePriceAlertRule(ctx context.Context, req *connect.Request[v1.UpdatePriceAlertRuleRequest]) (*connect.Response[v1.UpdatePriceAlertRuleResponse], error) {
	m := req.Msg
	if m.Id <= 0 {
		return connect.NewResponse(&v1.UpdatePriceAlertRuleResponse{Error: "id 必须 > 0"}), nil
	}
	sym, op, level, msg, errMsg := validatePriceAlertInput(m.Symbol, m.Op, m.Level, m.Message, m.Threshold)
	if errMsg != "" {
		return connect.NewResponse(&v1.UpdatePriceAlertRuleResponse{Error: errMsg}), nil
	}
	r, err := s.store.UpdatePriceAlertRule(ctx, m.Id, sym, op, m.Threshold, level, msg, m.Enabled)
	if err != nil {
		return nil, fmt.Errorf("update price alert rule: %w", err)
	}
	s.kickConfig() // D-469b S5
	return connect.NewResponse(&v1.UpdatePriceAlertRuleResponse{
		Ok:   true,
		Rule: s.priceAlertRowToProto(r),
	}), nil
}

// DeletePriceAlertRule 硬删规则，按 id。
func (s *Service) DeletePriceAlertRule(ctx context.Context, req *connect.Request[v1.DeletePriceAlertRuleRequest]) (*connect.Response[v1.DeletePriceAlertRuleResponse], error) {
	m := req.Msg
	if m.Id <= 0 {
		return connect.NewResponse(&v1.DeletePriceAlertRuleResponse{Error: "id 必须 > 0"}), nil
	}
	deleted, err := s.store.DeletePriceAlertRule(ctx, m.Id)
	if err != nil {
		return nil, fmt.Errorf("delete price alert rule: %w", err)
	}
	if !deleted {
		return connect.NewResponse(&v1.DeletePriceAlertRuleResponse{Error: "规则不存在"}), nil
	}
	s.kickConfig() // D-469b S5
	return connect.NewResponse(&v1.DeletePriceAlertRuleResponse{Ok: true}), nil
}
