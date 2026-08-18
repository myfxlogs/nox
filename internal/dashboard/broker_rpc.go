// Broker 管理 RPC handler（docs/broker管理器实现方案.md §2/§3）。
// 错误统一走 connect error；凭据明文存储（任务约束，见方案 §1）。
package dashboard

import (
	"context"
	"fmt"
	"strings"

	"arb/internal/adapter"
	"arb/internal/store/pgstore"

	mt4pb "arb/internal/adapter/gen/mtapi/mt4"
	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// AddBroker 验证并持久化账号，成功后动态启动适配器。
func (s *Service) AddBroker(ctx context.Context, req *connect.Request[v1.AddBrokerRequest]) (*connect.Response[v1.AddBrokerResponse], error) {
	venue := strings.TrimSpace(req.Msg.GetVenue())
	if venue == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("venue 不能为空"))
	}
	plat := req.Msg.GetPlatform()
	if plat != 0 && plat != 1 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("platform 必须为 0(MT4) 或 1(MT5)"))
	}
	acct := &pgstore.BrokerAccount{
		Venue:    venue,
		Platform: int(plat),
		Host:     req.Msg.GetHost(),
		Port:     int(req.Msg.GetPort()),
		User:     req.Msg.GetUser(),
		Password: req.Msg.GetPassword(),
		Server:   req.Msg.GetServer(),
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
func (s *Service) RemoveBroker(ctx context.Context, req *connect.Request[v1.RemoveBrokerRequest]) (*connect.Response[v1.RemoveBrokerResponse], error) {
	if err := s.bm.RemoveBroker(ctx, req.Msg.GetVenue()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.RemoveBrokerResponse{}), nil
}

// ListBrokerAccounts 返回全部已配置账号。
func (s *Service) ListBrokerAccounts(ctx context.Context, req *connect.Request[v1.ListBrokerAccountsRequest]) (*connect.Response[v1.ListBrokerAccountsResponse], error) {
	accounts, err := s.store.ListBrokerAccounts(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	out := make([]*v1.BrokerAccount, 0, len(accounts))
	for _, a := range accounts {
		acct := &v1.BrokerAccount{
			Venue:     a.Venue,
			Platform:  int32(a.Platform),
			Host:      a.Host,
			Port:      int32(a.Port),
			User:      a.User,
			Server:    a.Server,
			Alias:     a.Alias,
			Status:    a.Status,
			CreatedAt: timestamppb.New(a.CreatedAt),
		}
		// 账号真实佣金（bps/腿，D-036）；nil = 未设置回退默认
		acct.CommissionBps = a.CommissionBps
		out = append(out, acct)
	}
	return connect.NewResponse(&v1.ListBrokerAccountsResponse{Accounts: out}), nil
}

// VerifyAccount 临时连接并读账户摘要，验证凭据有效性。
func (s *Service) VerifyAccount(ctx context.Context, req *connect.Request[v1.VerifyAccountRequest]) (*connect.Response[v1.VerifyAccountResponse], error) {
	host := strings.TrimSpace(req.Msg.GetHost())
	if host == "" || req.Msg.GetUser() == 0 || req.Msg.GetPassword() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("host / 账号 / 密码不能为空"))
	}
	switch req.Msg.GetPlatform() {
	case 0:
		bal, eq, cur, lev, err := verifyMT4(ctx, host, req.Msg.GetServer(), int32(req.Msg.GetPort()), req.Msg.GetUser(), req.Msg.GetPassword())
		if err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, err)
		}
		return connect.NewResponse(&v1.VerifyAccountResponse{Balance: bal, Equity: eq, Currency: cur, Leverage: lev}), nil
	case 1:
		bal, eq, cur, lev, err := verifyMT5(ctx, host, req.Msg.GetServer(), int32(req.Msg.GetPort()), req.Msg.GetUser(), req.Msg.GetPassword())
		if err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, err)
		}
		return connect.NewResponse(&v1.VerifyAccountResponse{Balance: bal, Equity: eq, Currency: cur, Leverage: lev}), nil
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("platform 必须为 0(MT4) 或 1(MT5)"))
	}
}

func verifyMT4(ctx context.Context, host, server string, port int32, user int64, pass string) (float64, float64, string, int32, error) {
	a := adapter.NewMT4Adapter("", host, server, port, user, pass)
	if err := a.Connect(ctx); err != nil {
		return 0, 0, "", 0, fmt.Errorf("MT4 连接失败: %w", err)
	}
	defer a.Stop()
	resp, err := a.MT4Client().AccountSummary(tokenCtx(ctx, a.Token()), &mt4pb.AccountSummaryRequest{Id: a.Token()})
	if err != nil {
		return 0, 0, "", 0, fmt.Errorf("MT4 账户验证失败: %w", err)
	}
	if e := resp.GetError(); e != nil {
		return 0, 0, "", 0, fmt.Errorf("MT4 账户验证失败: %s", e.GetMessage())
	}
	r := resp.GetResult()
	return r.GetBalance(), r.GetEquity(), r.GetCurrency(), int32(r.GetLeverage()), nil
}

func verifyMT5(ctx context.Context, host, server string, port int32, user int64, pass string) (float64, float64, string, int32, error) {
	a := adapter.NewMT5Adapter("", host, server, port, user, pass)
	if err := a.Connect(ctx); err != nil {
		return 0, 0, "", 0, fmt.Errorf("MT5 连接失败: %w", err)
	}
	defer a.Stop()
	resp, err := a.MT5Client().AccountSummary(tokenCtx(ctx, a.Token()), &mt5pb.AccountSummaryRequest{Id: a.Token()})
	if err != nil {
		return 0, 0, "", 0, fmt.Errorf("MT5 账户验证失败: %w", err)
	}
	if e := resp.GetError(); e != nil {
		return 0, 0, "", 0, fmt.Errorf("MT5 账户验证失败: %s", e.GetMessage())
	}
	r := resp.GetResult()
	return r.GetBalance(), r.GetEquity(), r.GetCurrency(), int32(r.GetLeverage()), nil
}

// BrokerSymbols 返回运行中适配器的服务器符号表（附 broker 分组，补映射搜索用）。
func (s *Service) BrokerSymbols(ctx context.Context, req *connect.Request[v1.BrokerSymbolsRequest]) (*connect.Response[v1.BrokerSymbolsResponse], error) {
	symbols, err := s.bm.BrokerSymbols(ctx, req.Msg.GetVenue())
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	entries := make([]*v1.SymbolEntry, 0, len(symbols))
	for _, sg := range symbols {
		entries = append(entries, &v1.SymbolEntry{Symbol: sg.Symbol, Group: sg.Group})
	}
	return connect.NewResponse(&v1.BrokerSymbolsResponse{Symbols: entries}), nil
}

// SetBrokerAccountAlias 设置账号别名；空串 = 清除。
func (s *Service) SetBrokerAccountAlias(ctx context.Context, req *connect.Request[v1.SetBrokerAccountAliasRequest]) (*connect.Response[v1.SetBrokerAccountAliasResponse], error) {
	if err := s.store.SetBrokerAccountAlias(ctx, req.Msg.GetVenue(), req.Msg.GetAlias()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.SetBrokerAccountAliasResponse{}), nil
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
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.bm.SetCommission(venue, bps)
	return connect.NewResponse(&v1.SetBrokerCommissionResponse{}), nil
}

// SetBrokerDisabled 停用/启用账号（D-033）：停用停适配器保留凭据，启用重新连接。
func (s *Service) SetBrokerDisabled(ctx context.Context, req *connect.Request[v1.SetBrokerDisabledRequest]) (*connect.Response[v1.SetBrokerDisabledResponse], error) {
	venue := req.Msg.GetVenue()
	var err error
	if req.Msg.GetDisabled() {
		err = s.bm.DisableBroker(ctx, venue)
	} else {
		err = s.bm.EnableBroker(ctx, venue)
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.SetBrokerDisabledResponse{}), nil
}

// --- Symbol 映射 ---

// ListSymbolMappings 列出映射；venue 空 = 全部。
func (s *Service) ListSymbolMappings(ctx context.Context, req *connect.Request[v1.ListSymbolMappingsRequest]) (*connect.Response[v1.ListSymbolMappingsResponse], error) {
	mappings, err := s.store.ListSymbolMappings(ctx, req.Msg.GetVenue())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	pb := make([]*v1.SymbolMapping, 0, len(mappings))
	for _, mp := range mappings {
		pb = append(pb, &v1.SymbolMapping{Venue: mp.Venue, BrokerSymbol: mp.BrokerSymbol, Canonical: mp.Canonical})
	}
	return connect.NewResponse(&v1.ListSymbolMappingsResponse{Mappings: pb}), nil
}

// SaveSymbolMapping 写入映射；保存后重推适配器符号映射（运行时生效）。
func (s *Service) SaveSymbolMapping(ctx context.Context, req *connect.Request[v1.SaveSymbolMappingRequest]) (*connect.Response[v1.SaveSymbolMappingResponse], error) {
	brokerSym := strings.TrimSpace(req.Msg.GetBrokerSymbol())
	canonical := strings.TrimSpace(req.Msg.GetCanonical())
	if req.Msg.GetVenue() == "" || brokerSym == "" || canonical == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("venue / broker_symbol / canonical 不能为空"))
	}
	if err := s.store.UpsertSymbolMapping(ctx, &pgstore.SymbolMapping{
		Venue: req.Msg.GetVenue(), BrokerSymbol: brokerSym, Canonical: canonical,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.bm.ReloadSymbolMap(ctx, req.Msg.GetVenue())
	return connect.NewResponse(&v1.SaveSymbolMappingResponse{}), nil
}

// DeleteSymbolMapping 删除映射并重推适配器符号映射。
func (s *Service) DeleteSymbolMapping(ctx context.Context, req *connect.Request[v1.DeleteSymbolMappingRequest]) (*connect.Response[v1.DeleteSymbolMappingResponse], error) {
	if err := s.store.DeleteSymbolMapping(ctx, req.Msg.GetVenue(), req.Msg.GetBrokerSymbol()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.bm.ReloadSymbolMap(ctx, req.Msg.GetVenue())
	return connect.NewResponse(&v1.DeleteSymbolMappingResponse{}), nil
}
