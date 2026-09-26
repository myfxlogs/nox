// service_allowrules_admin.go D-492 S3：autotrade 执行白名单增删 RPC——
// simulated 服务端推导（禁前端传：CEX=exchange_configs 显式 WHERE simulated=$env 过滤
// 【KB exchange-config-pick-simulated-filter：禁取首行】；MT=broker_accounts is_real
// 三态；canonical=env 推导+无采集 warn 不拒）；去重幂等（enabled 行幂等 ok、disabled
// 行复用 SetEnabled 翻转）；写后 info 审计+kickConfig 热载（Set 同径）。
package dashboard

import (
	"arb/internal/keydomain"
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"connectrpc.com/connect"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
)

// D-492 value 硬校验（venue_key 全形唯一口径，禁裸 venue——KB swap-snapshot-venuekey-mismatch）：
// CEX=小写 venue:spot|perp；MT=纯数字 venue:mt5（mt4 已退役 D-470——格式层拒）；
// canonical=大写字母数字（原文不译不规范化）。
var (
	cexVenueKeyRe  = regexp.MustCompile(`^[a-z0-9]+:(spot|perp)$`)
	mtVenueKeyRe   = regexp.MustCompile(`^[0-9]+:mt5$`)
	canonicalKeyRe = regexp.MustCompile(`^[A-Z0-9]{2,20}$`)
)

// AddAutotradeAllowRule 增规则（幂等）：校验→simulated 推导→去重→插/复用→审计+热载。
// 全程 in-band ok=false 拒收（管理面错误前端可直显）；内部错误才 connect error。
func (s *Service) AddAutotradeAllowRule(ctx context.Context, req *connect.Request[v1.AddAutotradeAllowRuleRequest]) (*connect.Response[v1.AddAutotradeAllowRuleResponse], error) {
	g := req.Msg
	resp := &v1.AddAutotradeAllowRuleResponse{}
	fail := func(format string, a ...any) (*connect.Response[v1.AddAutotradeAllowRuleResponse], error) {
		resp.Error = fmt.Sprintf(format, a...)
		return connect.NewResponse(resp), nil
	}

	kind, value, note := strings.TrimSpace(g.Kind), strings.TrimSpace(g.Value), strings.TrimSpace(g.Note)
	if kind != "venue" && kind != "canonical" {
		return fail("kind 只接受 venue/canonical（收到 %q）", kind)
	}
	if note == "" {
		return fail("note 必填（权限变更强制审计备注）")
	}
	// simulated 推导（env 语义与 LoadAutotradeAllowRules 消费侧一致：demo→sim 行，其余→real 行）
	wantSim := s.environment == "demo"
	simulated, derr := s.deriveRuleSimulated(ctx, kind, value, wantSim)
	if derr != "" {
		return fail("%s", derr)
	}
	warning := ""
	if kind == "canonical" {
		seen, err := s.store.CanonicalSeenAnywhere(ctx, value)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		if !seen {
			warning = "canonical 无采集记录（可能拼写错误或尚未上线采集）"
		}
	}

	// 去重：同 (kind,value,simulated) 已存在 → enabled 幂等 ok / disabled 复用翻转。
	existing, err := s.store.FindAutotradeAllowRule(ctx, kind, value, simulated)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if existing != nil {
		resp.Id = &existing.ID
		if existing.Enabled {
			resp.Ok = true
			resp.Warning = joinWarn(warning, "行已存在且启用中（幂等，未新插）")
			return connect.NewResponse(resp), nil
		}
		if _, err := s.store.SetAutotradeAllowRuleEnabled(ctx, existing.ID, true, note); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		resp.Ok, resp.ReenabledExisting = true, true
		resp.Warning = warning
		s.auditAllowRule(ctx, fmt.Sprintf("[whitelist] reenable autotrade rule #%d %s %s note=%s", existing.ID, kind, value, note))
		return connect.NewResponse(resp), nil
	}

	inserted, err := s.store.InsertAutotradeAllowRule(ctx, kind, value, simulated, note)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if inserted == nil {
		// ON CONFLICT 空拳——并发/竞态下同 key 已有行；回查复用（enabled=true 是新插语义）。
		again, aerr := s.store.FindAutotradeAllowRule(ctx, kind, value, simulated)
		if aerr != nil || again == nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("insert 撞键后回查失败: %v", aerr))
		}
		resp.Id = &again.ID
		resp.Ok = true
		resp.Warning = joinWarn(warning, "并发撞键：行已存在（幂等）")
		return connect.NewResponse(resp), nil
	}
	resp.Id, resp.Ok, resp.Warning = &inserted.ID, true, warning
	s.auditAllowRule(ctx, fmt.Sprintf("[whitelist] add autotrade rule #%d %s %s sim=%v note=%s", inserted.ID, kind, value, simulated, note))
	return connect.NewResponse(resp), nil
}

// DeleteAutotradeAllowRule 删规则：只允许删 enabled=false 行（活通道先停用再删，防一步误删）。
func (s *Service) DeleteAutotradeAllowRule(ctx context.Context, req *connect.Request[v1.DeleteAutotradeAllowRuleRequest]) (*connect.Response[v1.DeleteAutotradeAllowRuleResponse], error) {
	g := req.Msg
	resp := &v1.DeleteAutotradeAllowRuleResponse{}
	row, err := s.store.AutotradeAllowRuleByID(ctx, g.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if row == nil {
		resp.Error = fmt.Sprintf("规则 #%d 不存在", g.Id)
		return connect.NewResponse(resp), nil
	}
	if row.Enabled {
		resp.Error = "启用中行禁删——先停用再删（防一步误删活通道）"
		return connect.NewResponse(resp), nil
	}
	if err := s.store.DeleteAutotradeAllowRule(ctx, g.Id); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.auditAllowRule(ctx, fmt.Sprintf("[whitelist] delete autotrade rule #%d %s %s note=%s", row.ID, row.Kind, row.Value, strings.TrimSpace(g.Note)))
	resp.Ok = true
	return connect.NewResponse(resp), nil
}

// deriveRuleSimulated kind 分流推导 simulated；返回错误串=""+in-band 拒收（禁猜）。
func (s *Service) deriveRuleSimulated(ctx context.Context, kind, value string, wantSim bool) (bool, string) {
	switch kind {
	case "venue":
		if mtVenueKeyRe.MatchString(value) {
			// MT：broker_accounts venue=数字主名 + platform=1（mt5）；is_real 三态
			// nil=未知拒（未知实盘属性禁入真规则域）；推导与环境不匹配拒。
			venue := keydomain.BareOf(value)
			isReal, err := s.store.BrokerAccountIsReal(ctx, venue, 1)
			if err != nil {
				return false, "账号查询失败: " + err.Error()
			}
			if isReal == nil {
				return false, fmt.Sprintf("%s 无 MT5 账号或 is_real 未知——禁猜", value)
			}
			if sim := !*isReal; sim != wantSim {
				return false, fmt.Sprintf("%s 账号实为%s，与当前环境 %s 规则域不匹配", value, map[bool]string{true: "模拟", false: "实盘"}[sim], s.environment)
			}
			return !*isReal, ""
		}
		if !cexVenueKeyRe.MatchString(value) {
			return false, fmt.Sprintf("venue value 格式非法 %q——需 venue:spot|perp（CEX）或 数字:mt5（MT；mt4 已退役）", value)
		}
		// CEX：exchange_configs (venue,venue_type,simulated=wantSim) 显式过滤——
		// 无匹配行=该环境无配置拒（同 venue:type 双配置并存按 env 取对应行）。
		vk := keydomain.Parse(value)
		ok, err := s.store.ExchangeConfigHasRow(ctx, vk.Venue, vk.Type, wantSim)
		if err != nil {
			return false, "配置查询失败: " + err.Error()
		}
		if !ok {
			return false, fmt.Sprintf("%s 无%s配置行——禁止猜 simulated", value, map[bool]string{true: "模拟", false: "实盘"}[wantSim])
		}
		return wantSim, ""
	case "canonical":
		if !canonicalKeyRe.MatchString(value) {
			return false, fmt.Sprintf("canonical value 格式非法 %q——需大写字母数字 2-20 位（如 SKLUSDT）", value)
		}
		return wantSim, "" // canonical 按运行域定（规则本就按 env 分流加载）
	}
	return false, "kind 非法"
}

// auditAllowRule 增删审计告警（Set 路径同格式；best-effort 不回滚）+kickConfig 热载。
func (s *Service) auditAllowRule(ctx context.Context, text string) {
	if err := s.store.InsertAlert(ctx, nil, "info", text); err != nil {
		slog.Warn("allow rule audit alert fail", "err", err)
	}
	s.kickConfig()
}

func joinWarn(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "；" + b
}
