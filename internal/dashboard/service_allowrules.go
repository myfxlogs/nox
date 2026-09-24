// service_allowrules.go D-444 S3：autotrade venue/canonical 执行白名单启停双 RPC。
// 行只读列出 + enabled 翻转（增删走业主 SQL=SSOT §2.1）；翻转写 info 审计告警
// （InsertAlert best-effort：告警失败不回滚已生效翻转，同毕业链 approve 先例）。
package dashboard

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"google.golang.org/protobuf/types/known/timestamppb"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
)

// ListAutotradeAllowRules 全量行直读（kind,value 序后端已排）。
// D-445：venue 行附实时资金列——acctMon.Refresh 活查为主源（updated_at≈now 常态胜），
// MT5 OnOrderUpdate 流帧为 Refresh 失败/离线兜底+轮询间隙加温；两源 per-venue 取较新者。
func (s *Service) ListAutotradeAllowRules(ctx context.Context, _ *connect.Request[v1.ListAutotradeAllowRulesRequest]) (*connect.Response[v1.ListAutotradeAllowRulesResponse], error) {
	rules, err := s.store.ListAutotradeAllowRulesAll(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	accts := map[string]*v1.AccountInfo{}
	if s.acctMon != nil {
		// Refresh 不包额外 WithTimeout：querier 自带超时，ctx 提前取消会把未查 venue
		// 误标 offline 污染共享态+喂 cooldown；典型延迟 2-8s 由 RPC 自然承担。
		for _, a := range s.acctMon.Refresh(ctx) {
			vk, _, _ := strings.Cut(a.GetVenueKey(), "#") // D-362 撞键 #id 后缀归一
			// 撞键取 simulated=false 优先（CEX demo/real 同 venue:type 场景）。
			if prev, ok := accts[vk]; !ok || (prev.GetSimulated() && !a.GetSimulated()) {
				accts[vk] = a
			}
		}
	}
	resp := &v1.ListAutotradeAllowRulesResponse{Rules: make([]*v1.AutotradeAllowRuleRow, 0, len(rules))}
	for _, r := range rules {
		row := &v1.AutotradeAllowRuleRow{
			Id: r.ID, Kind: r.Kind, Value: r.Value,
			Enabled: r.Enabled, Simulated: r.Simulated, Note: r.Note,
		}
		if r.Kind == "venue" && s.acctMon != nil {
			fillRuleAcct(row, r.Value, accts[r.Value], s.acctMon)
		}
		resp.Rules = append(resp.Rules, row)
	}
	return connect.NewResponse(resp), nil
}

// fillRuleAcct D-445：Refresh 结果 vs 流帧取较新者填资金列；均 miss → has_account=false。
func fillRuleAcct(row *v1.AutotradeAllowRuleRow, venueKey string, a *v1.AccountInfo, mon *AccountMonitor) {
	var sa StreamAcct
	hasStream := false
	if mon != nil {
		sa, hasStream = mon.StreamAccount(venueKey)
	}
	if a != nil {
		// UpdatedAt nil→视为最旧（不 panic：AsTime nil-safe 得 epoch，但显式判断防误胜）。
		updNil := a.GetUpdatedAt() == nil
		if hasStream && (updNil || sa.FrameTime.After(a.GetUpdatedAt().AsTime())) {
			// 流帧较新（Refresh 失败期/轮询间隙）：流值填入，online=true（帧在流=连接活）。
			// currency 流帧恒空（OrderUpdateSummary 无该字段）→回落 Refresh 的账户级币种（不变属性）。
			row.Balance, row.Equity = sa.Balance, sa.Equity
			row.Currency = sa.Currency
			if row.Currency == "" {
				row.Currency = a.GetCurrency()
			}
			row.Online = true
			row.AcctUpdatedAt = timestamppb.New(sa.FrameTime)
		} else {
			row.Balance, row.Equity, row.Currency = a.GetBalance(), a.GetEquity(), a.GetCurrency()
			row.Online = a.GetOnline()
			row.AcctUpdatedAt = a.GetUpdatedAt()
		}
		row.HasAccount = true
		return
	}
	if hasStream {
		// Refresh 全 miss（querier 未注册/冷却）→ 流帧兜底。
		row.Balance, row.Equity, row.Currency = sa.Balance, sa.Equity, sa.Currency
		row.Online = true
		row.AcctUpdatedAt = timestamppb.New(sa.FrameTime)
		row.HasAccount = true
	}
}

// SetAutotradeAllowRuleEnabled 翻转 enabled + note 可附 + info 审计告警（权限变更必有痕）。
func (s *Service) SetAutotradeAllowRuleEnabled(ctx context.Context, req *connect.Request[v1.SetAutotradeAllowRuleEnabledRequest]) (*connect.Response[v1.SetAutotradeAllowRuleEnabledResponse], error) {
	r, err := s.store.SetAutotradeAllowRuleEnabled(ctx, req.Msg.Id, req.Msg.Enabled, req.Msg.Note)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("autotrade_allow_rules id=%d 不存在", req.Msg.Id))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	noteSuffix := ""
	if req.Msg.Note != "" {
		noteSuffix = " note=" + req.Msg.Note
	}
	if aerr := s.store.InsertAlert(ctx, nil, "info",
		fmt.Sprintf("[whitelist] autotrade rule #%d %s %s → enabled=%v%s", r.ID, r.Kind, r.Value, r.Enabled, noteSuffix)); aerr != nil {
		slog.Warn("allow rule audit alert fail", "id", r.ID, "err", aerr) // best-effort 不回滚
	}
	return connect.NewResponse(&v1.SetAutotradeAllowRuleEnabledResponse{Ok: true}), nil
}
