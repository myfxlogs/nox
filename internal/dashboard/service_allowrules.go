// service_allowrules.go D-444 S3：autotrade venue/canonical 执行白名单启停双 RPC。
// 行只读列出 + enabled 翻转（增删走业主 SQL=SSOT §2.1）；翻转写 info 审计告警
// （InsertAlert best-effort：告警失败不回滚已生效翻转，同毕业链 approve 先例）。
package dashboard

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
)

// ListAutotradeAllowRules 全量行直读（kind,value 序后端已排）。
func (s *Service) ListAutotradeAllowRules(ctx context.Context, _ *connect.Request[v1.ListAutotradeAllowRulesRequest]) (*connect.Response[v1.ListAutotradeAllowRulesResponse], error) {
	rules, err := s.store.ListAutotradeAllowRulesAll(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &v1.ListAutotradeAllowRulesResponse{Rules: make([]*v1.AutotradeAllowRuleRow, 0, len(rules))}
	for _, r := range rules {
		resp.Rules = append(resp.Rules, &v1.AutotradeAllowRuleRow{
			Id: r.ID, Kind: r.Kind, Value: r.Value,
			Enabled: r.Enabled, Simulated: r.Simulated, Note: r.Note,
		})
	}
	return connect.NewResponse(resp), nil
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
