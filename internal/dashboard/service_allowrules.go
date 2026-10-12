// service_allowrules.go D-444 S3：autotrade venue/canonical 执行白名单启停双 RPC。
// 行只读列出 + enabled 翻转（D-492 起增删走 UI，service_allowrules_admin.go）；翻转写 info 审计告警
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

	v1 "arb/internal/gen/dashboardv1"
)

// ListAutotradeAllowRules 全量行直读（kind,value 序后端已排）。
// D-445/D-447：venue 行附实时资金列——acctMon.Snapshot 为主源（monitor 常驻 5s 轮询
// +流帧 tap 的活缓存，updated_at≈now 常态胜）；自动轮询场景下 Refresh=每调一次重复远端
// fan-out 与后台循环双重负载（D-447）。MT5 OnOrderUpdate 流帧为兜底+加温；两源取较新者。
func (s *Service) ListAutotradeAllowRules(ctx context.Context, _ *connect.Request[v1.ListAutotradeAllowRulesRequest]) (*connect.Response[v1.ListAutotradeAllowRulesResponse], error) {
	rules, err := s.store.ListAutotradeAllowRulesAll(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	// D-504 S2：demo 域残留收管理面——venue 规则指向 is_real=false 户则不列
	// （物理行留库；canonical 规则无 venue 属性不受滤；is_real 未知=nil 保守列出；
	// 执行侧 LoadAutotradeAllowRules 语义不动——live env 检查本已拦 demo venue）。
	isReal, rerr := s.store.ListVenueIsReal(ctx)
	if rerr == nil {
		filtered := rules[:0]
		for _, r := range rules {
			if r.Kind == "venue" {
				if v, ok := isReal[r.Value]; ok && v != nil && !*v {
					continue
				}
			}
			filtered = append(filtered, r)
		}
		rules = filtered
	}
	accts := map[string]*v1.AccountInfo{}
	if s.acctMon != nil {
		for _, a := range s.acctMon.Snapshot() {
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

// fillRuleAcct D-445/D-447：快照 vs 流帧取较新者填资金列；均 miss → has_account=false。
func fillRuleAcct(row *v1.AutotradeAllowRuleRow, venueKey string, a *v1.AccountInfo, mon *AccountMonitor) {
	var sa StreamAcct
	hasStream := false
	if mon != nil {
		sa, hasStream = mon.StreamAccount(venueKey)
	}
	if a != nil {
		// D-448：账号级 demo/real 为不变属性——a 在时恒取快照侧 mode（流帧无该信息）。
		row.AcctMode = "real"
		if a.GetSimulated() {
			row.AcctMode = "demo"
		}
		// UpdatedAt nil→视为最旧（不 panic：AsTime nil-safe 得 epoch，但显式判断防误胜）。
		updNil := a.GetUpdatedAt() == nil
		if hasStream && (updNil || sa.FrameTime.After(a.GetUpdatedAt().AsTime())) {
			// 流帧较新（轮询间隙/缓存空窗）：流值填入，online=true（帧在流=连接活）。
			// currency 流帧恒空（OrderUpdateSummary 无该字段）→回落快照的账户级币种（不变属性）。
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
		// 快照全 miss（querier 未注册/冷却/boot 首 poll 前）→ 流帧兜底。
		// acct_mode 恒 ""——demo 户被误标 real 比空危险，诚实空前端显「—」。
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
		fmt.Sprintf("[白名单] 自动交易规则 #%d %s %s → enabled=%v%s", r.ID, r.Kind, r.Value, r.Enabled, noteSuffix)); aerr != nil {
		slog.Warn("allow rule audit alert fail", "id", r.ID, "err", aerr) // best-effort 不回滚
	}
	s.kickConfig() // D-469b S5：allow rules 热载 kick
	s.PublishDataRefresh(RefreshAllowRules)
	return connect.NewResponse(&v1.SetAutotradeAllowRuleEnabledResponse{Ok: true}), nil
}
