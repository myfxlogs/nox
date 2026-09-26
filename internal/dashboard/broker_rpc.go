// Broker 管理 RPC handler（docs/broker管理器实现方案.md §2/§3）。
// 错误统一走 connect error；凭据明文存储（任务约束，见方案 §1）。
package dashboard

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"arb/internal/store/pgstore"

	"arb/internal/adapter"
	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// AddBroker 验证并持久化账号，成功后动态启动适配器。
// D-141 S5：host 支持逗号分隔多端点，failover 解析胜出端点后落库（存单端点，适配器直连）。
func (s *Service) AddBroker(ctx context.Context, req *connect.Request[v1.AddBrokerRequest]) (*connect.Response[v1.AddBrokerResponse], error) {
	venue := strings.TrimSpace(req.Msg.GetVenue())
	if venue == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("venue 不能为空"))
	}
	plat := req.Msg.GetPlatform()
	if plat != 1 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("MT4 已退役：platform 仅支持 1(MT5)")) // D-470
	}
	// D-141 S5：多端点 failover——解析胜出 host+port 后落库。D-142 ① PingHost 预排序。
	_, _, _, _, winHost, winPort, _, err := verifyMT5(ctx, req.Msg.GetHost(), req.Msg.GetServer(), int32(req.Msg.GetPort()), req.Msg.GetUser(), req.Msg.GetPassword())
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	acct := &pgstore.BrokerAccount{
		Venue:    venue,
		Platform: int(plat),
		Host:     winHost,
		Port:     int(winPort),
		User:     req.Msg.GetUser(),
		Password: req.Msg.GetPassword(),
		Server:   req.Msg.GetServer(),
		IsReal:   req.Msg.IsReal,       // D-144 ① 三态：*bool（nil=未知）
		Website:  req.Msg.GetWebsite(), // D-157 V3：broker 官网
	}
	// 账号类型真实佣金（bps/腿，D-036）；缺省 = 回退 config 默认
	if req.Msg.CommissionBps != nil {
		v := req.Msg.GetCommissionBps()
		acct.CommissionBps = &v
	}
	if err := s.bm.AddBroker(ctx, acct); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return connect.NewResponse(&v1.AddBrokerResponse{}), nil
}

// RemoveBroker 停止适配器并删除账号。
// D-493 S5：删除前写 execution_control_events 审计（scope_type='broker', actor='ui', reason 来自 UI）。
func (s *Service) RemoveBroker(ctx context.Context, req *connect.Request[v1.RemoveBrokerRequest]) (*connect.Response[v1.RemoveBrokerResponse], error) {
	venue := req.Msg.GetVenue()
	fromMode := ""
	if a, err := s.store.GetBrokerAccount(ctx, venue); err == nil && a != nil {
		fromMode = a.Status
	}
	if err := s.store.AppendExecutionControlEvent(ctx, &pgstore.ExecutionControlEvent{
		ScopeType: "broker",
		ScopeKey:  venue,
		FromMode:  fromMode,
		ToMode:    "removed",
		Actor:     "ui",
		Reason:    req.Msg.GetReason(),
		EventKey:  fmt.Sprintf("broker-remove-%s-%d", venue, time.Now().UnixNano()),
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("write remove audit: %w", err))
	}
	if err := s.bm.RemoveBroker(ctx, venue); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.RemoveBrokerResponse{}), nil
}

// UpdateBrokerCredentials D-493 S4：凭据编辑（空串/0=不改动；password 永不回传）。
func (s *Service) UpdateBrokerCredentials(ctx context.Context, req *connect.Request[v1.UpdateBrokerCredentialsRequest]) (*connect.Response[v1.UpdateBrokerCredentialsResponse], error) {
	if err := s.bm.UpdateBrokerCredentials(ctx, req.Msg.GetVenue(), req.Msg.GetHost(), int(req.Msg.GetPort()), req.Msg.GetServer(), req.Msg.GetPassword()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.UpdateBrokerCredentialsResponse{}), nil
}

// ListBrokerAccounts 返回全部已配置账号。
func (s *Service) ListBrokerAccounts(ctx context.Context, req *connect.Request[v1.ListBrokerAccountsRequest]) (*connect.Response[v1.ListBrokerAccountsResponse], error) {
	accounts, err := s.store.ListBrokerAccounts(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	// D-493 S1：报价健康透出（qbus 同源，无新轮询）；查询失败回退空表不阻断列表。
	quoteAges := quoteAgeByVenue(s.qbus, time.Now())
	mapCounts, err := s.store.VenueSymbolMapCounts(ctx)
	if err != nil {
		log.Printf("[broker] symbol map counts: %v", err)
		mapCounts = nil
	}
	out := make([]*v1.BrokerAccount, 0, len(accounts))
	for _, a := range accounts {
		feedState, quoteAgeSec := feedStateOf(quoteAges[a.Venue])
		acct := &v1.BrokerAccount{
			Venue:           a.Venue,
			Platform:        int32(a.Platform),
			Host:            a.Host,
			Port:            int32(a.Port),
			User:            a.User,
			Server:          a.Server,
			Alias:           a.Alias,
			Status:          a.Status,
			CreatedAt:       timestamppb.New(a.CreatedAt),
			IsReal:          a.IsReal,              // D-144 ① 三态：*bool（nil=未知）
			ServerGroup:     serverGroup(a.Server), // D-150 ② 平台分组键后端算好下发
			Website:         a.Website,             // D-157 V3：broker 官网
			FeedState:       feedState,
			LastQuoteAgeSec: quoteAgeSec,
			SymbolMapCount:  int32(mapCounts[a.Venue]),
		}
		// 账号真实佣金（bps/腿，D-036）；nil = 未设置回退默认
		acct.CommissionBps = a.CommissionBps
		out = append(out, acct)
	}
	return connect.NewResponse(&v1.ListBrokerAccountsResponse{Accounts: out}), nil
}

// VerifyAccount 临时连接并读账户摘要，验证凭据有效性。
// D-141 S5：host 支持逗号分隔多端点，滤私网后依次试连，胜出 host 回传。
func (s *Service) VerifyAccount(ctx context.Context, req *connect.Request[v1.VerifyAccountRequest]) (*connect.Response[v1.VerifyAccountResponse], error) {
	host := strings.TrimSpace(req.Msg.GetHost())
	if host == "" || req.Msg.GetUser() == 0 || req.Msg.GetPassword() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("host / 账号 / 密码不能为空"))
	}
	if req.Msg.GetPlatform() != 1 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("MT4 已退役：platform 仅支持 1(MT5)")) // D-470
	}
	bal, eq, cur, lev, winHost, winPort, lat, err := verifyMT5(ctx, host, req.Msg.GetServer(), int32(req.Msg.GetPort()), req.Msg.GetUser(), req.Msg.GetPassword())
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return connect.NewResponse(&v1.VerifyAccountResponse{Balance: bal, Equity: eq, Currency: cur, Leverage: lev, Host: winHost, Port: winPort, LatencyMs: lat}), nil
}

// verifyMT5 D-141 S5：多端点 failover。D-142 ① PingHost 预排序。
func verifyMT5(ctx context.Context, host, server string, port int32, user int64, pass string) (float64, float64, string, int32, string, int32, int32, error) {
	eps, err := parsePublicEndpoints(host, port)
	if err != nil {
		return 0, 0, "", 0, "", 0, 0, err
	}
	eps = pingSortViaGateway(ctx, 1, eps)
	r, ferr := failoverVerify(eps, func(ep endpoint) (failoverResult, error) {
		a := adapter.NewMT5Adapter("", ep.host, server, ep.port, user, pass)
		if cerr := a.Connect(ctx); cerr != nil {
			a.Stop()
			return failoverResult{}, cerr
		}
		resp, rerr := a.MT5Client().AccountSummary(tokenCtx(ctx, a.Token()), &mt5pb.AccountSummaryRequest{Id: a.Token()})
		if rerr != nil {
			a.Stop()
			return failoverResult{}, rerr
		}
		if e := resp.GetError(); e != nil {
			a.Stop()
			return failoverResult{}, fmt.Errorf("MT5 账户验证失败: %s", e.GetMessage())
		}
		res := resp.GetResult()
		out := failoverResult{
			balance: res.GetBalance(), equity: res.GetEquity(),
			currency: res.GetCurrency(), leverage: int32(res.GetLeverage()),
			host: ep.host, port: ep.port, latency: ep.latency,
		}
		a.Stop()
		return out, nil
	})
	if ferr != nil {
		return 0, 0, "", 0, "", 0, 0, fmt.Errorf("MT5 %w", ferr)
	}
	return r.balance, r.equity, r.currency, r.leverage, r.host, r.port, r.latency, nil
}

// BrokerSymbols 返回运行中适配器的服务器符号表（附 broker 分组，补映射搜索用）。
func (s *Service) BrokerSymbols(ctx context.Context, req *connect.Request[v1.BrokerSymbolsRequest]) (*connect.Response[v1.BrokerSymbolsResponse], error) {
	symbols, err := s.bm.BrokerSymbols(ctx, req.Msg.GetVenue())
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	entries := make([]*v1.SymbolEntry, 0, len(symbols))
	for _, sg := range symbols {
		entries = append(entries, &v1.SymbolEntry{Symbol: sg.Symbol, Group: sg.Group, ContractSize: sg.ContractSize})
	}
	return connect.NewResponse(&v1.BrokerSymbolsResponse{Symbols: entries}), nil
}

// brokerStoreErr D-388 S0.2：venue 不存在（0 行命中）映射 CodeNotFound——
// 静默无操作以 2xx 假成功收尾是前端停用点击失败的嫌疑路径之一，硬化后错误显眼上抛。
func brokerStoreErr(err error) error {
	if errors.Is(err, pgstore.ErrBrokerAccountNotFound) {
		return connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}

// SetBrokerAccountAlias 设置账号别名；空串 = 清除。
func (s *Service) SetBrokerAccountAlias(ctx context.Context, req *connect.Request[v1.SetBrokerAccountAliasRequest]) (*connect.Response[v1.SetBrokerAccountAliasResponse], error) {
	if err := s.store.SetBrokerAccountAlias(ctx, req.Msg.GetVenue(), req.Msg.GetAlias()); err != nil {
		return nil, brokerStoreErr(err)
	}
	return connect.NewResponse(&v1.SetBrokerAccountAliasResponse{}), nil
}

// SetBrokerWebsite D-157 V3.1：设置 broker 官网；空串 = 清除。
func (s *Service) SetBrokerWebsite(ctx context.Context, req *connect.Request[v1.SetBrokerWebsiteRequest]) (*connect.Response[v1.SetBrokerWebsiteResponse], error) {
	if err := s.store.SetBrokerWebsite(ctx, req.Msg.GetVenue(), req.Msg.GetWebsite()); err != nil {
		return nil, brokerStoreErr(err)
	}
	return connect.NewResponse(&v1.SetBrokerWebsiteResponse{}), nil
}

// SetBrokerAccountIsReal D-144 ① 三态：设置账号实盘标记。is_real 缺省=清除回未知(NULL)。
func (s *Service) SetBrokerAccountIsReal(ctx context.Context, req *connect.Request[v1.SetBrokerAccountIsRealRequest]) (*connect.Response[v1.SetBrokerAccountIsRealResponse], error) {
	if err := s.store.SetBrokerAccountIsReal(ctx, req.Msg.GetVenue(), req.Msg.IsReal); err != nil {
		return nil, brokerStoreErr(err)
	}
	return connect.NewResponse(&v1.SetBrokerAccountIsRealResponse{}), nil
}

// SetBrokerCommission 更新账号真实佣金（bps/腿，D-036）：持久化 + 运行时热生效。
// nil = 清除（回退 config 默认）；显式传值（含 0 = 免佣金账号）精确生效。
func (s *Service) SetBrokerCommission(ctx context.Context, req *connect.Request[v1.SetBrokerCommissionRequest]) (*connect.Response[v1.SetBrokerCommissionResponse], error) {
	venue := strings.TrimSpace(req.Msg.GetVenue())
	if venue == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("venue 不能为空"))
	}
	var bps *float64
	if req.Msg.CommissionBps != nil {
		v := req.Msg.GetCommissionBps()
		bps = &v
	}
	if err := s.store.SetBrokerCommission(ctx, venue, bps); err != nil {
		return nil, brokerStoreErr(err)
	}
	s.bm.SetCommission(venue, bps)
	return connect.NewResponse(&v1.SetBrokerCommissionResponse{}), nil
}

// SetBrokerDisabled 停用/启用账号（D-033）：停用停适配器保留凭据，启用重新连接。
func (s *Service) SetBrokerDisabled(ctx context.Context, req *connect.Request[v1.SetBrokerDisabledRequest]) (*connect.Response[v1.SetBrokerDisabledResponse], error) {
	venue := req.Msg.GetVenue()
	// D-388 S0.0：RPC 入口审计日志——「handler 是否执行」从此永远可观测（R0-A1：
	// 负证据只能证「未留可观测痕迹」不能证「未到达 handler」，本行关闭该观测洞）。
	log.Printf("[broker] rpc set-disabled venue=%s disabled=%v", venue, req.Msg.GetDisabled())
	var err error
	if req.Msg.GetDisabled() {
		err = s.bm.DisableBroker(ctx, venue)
	} else {
		err = s.bm.EnableBroker(ctx, venue)
	}
	if err != nil {
		return nil, brokerStoreErr(err)
	}
	return connect.NewResponse(&v1.SetBrokerDisabledResponse{}), nil
}
