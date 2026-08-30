// CEX 交易所管理 RPC handler（docs/CEX交易所后台管理.md D-054）。
// 对齐 broker_rpc.go：后端校验（D-043）、secret 只进后端不回传、错误走 connect。
package dashboard

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"arb/internal/store/pgstore"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// maskKey 掩码 API key：保留前 4 字符，其余以 *** 替代（secret/passphrase 永不回传）。
func maskKey(k string) string {
	if len(k) <= 4 {
		return "***"
	}
	return k[:4] + "***"
}

func exchangeToProto(c *pgstore.ExchangeConfig) *v1.ExchangeConfig {
	return &v1.ExchangeConfig{
		Id:        strconv.FormatInt(c.ID, 10),
		Venue:     c.Venue,
		VenueType: c.VenueType,
		Label:     c.Label,
		ApiKey:    maskKey(c.APIKey),
		BaseUrl:   c.BaseURL,
		Simulated: c.Simulated,
		Website:   c.Website, // D-157 V3：交易所官网
		CreatedAt: timestamppb.New(c.CreatedAt),
	}
}

// ListExchanges 返回全部已配置 CEX 交易所（api_key 掩码，secret 不回传）。
func (s *Service) ListExchanges(ctx context.Context, req *connect.Request[v1.ListExchangesRequest]) (*connect.Response[v1.ListExchangesResponse], error) {
	cfgs, err := s.store.ListExchangeConfigs(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	out := make([]*v1.ExchangeConfig, 0, len(cfgs))
	for i := range cfgs {
		out = append(out, exchangeToProto(&cfgs[i]))
	}
	return connect.NewResponse(&v1.ListExchangesResponse{Exchanges: out}), nil
}

// AddExchange 验证并持久化交易所配置；成功后下轮 AccountMonitor poll 自动生效（D-054）。
func (s *Service) AddExchange(ctx context.Context, req *connect.Request[v1.AddExchangeRequest]) (*connect.Response[v1.AddExchangeResponse], error) {
	venue := strings.TrimSpace(req.Msg.GetVenue())
	if venue == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("venue 不能为空"))
	}
	venueType := strings.TrimSpace(req.Msg.GetVenueType())
	if venueType == "" {
		venueType = "spot"
	}
	if strings.TrimSpace(req.Msg.GetApiKey()) == "" || strings.TrimSpace(req.Msg.GetApiSecret()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("api_key 与 api_secret 不能为空"))
	}
	c := &pgstore.ExchangeConfig{
		Venue:      venue,
		VenueType:  venueType,
		Label:      strings.TrimSpace(req.Msg.GetLabel()),
		APIKey:     strings.TrimSpace(req.Msg.GetApiKey()),
		APISecret:  strings.TrimSpace(req.Msg.GetApiSecret()),
		Passphrase: strings.TrimSpace(req.Msg.GetPassphrase()),
		BaseURL:    strings.TrimSpace(req.Msg.GetBaseUrl()),
		Simulated:  req.Msg.GetSimulated(),
		Website:    strings.TrimSpace(req.Msg.GetWebsite()), // D-157 V3：交易所官网
	}
	if err := s.store.InsertExchangeConfig(ctx, c); err != nil {
		if pgstore.ErrExchangeConflict(err) {
			return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("该交易所类型已配置：%s:%s", venue, venueType))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.AddExchangeResponse{Exchange: exchangeToProto(c)}), nil
}

// RemoveExchange 删除交易所配置；下轮 poll 停止该 querier。
func (s *Service) RemoveExchange(ctx context.Context, req *connect.Request[v1.RemoveExchangeRequest]) (*connect.Response[v1.RemoveExchangeResponse], error) {
	id, err := strconv.ParseInt(req.Msg.GetId(), 10, 64)
	if err != nil || id <= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("id 非法"))
	}
	if err := s.store.DeleteExchangeConfig(ctx, id); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.RemoveExchangeResponse{}), nil
}

// SetExchangeLabel 设置交易所别名；空串 = 清除（D-157 V3）。
func (s *Service) SetExchangeLabel(ctx context.Context, req *connect.Request[v1.SetExchangeLabelRequest]) (*connect.Response[v1.SetExchangeLabelResponse], error) {
	id, err := strconv.ParseInt(req.Msg.GetId(), 10, 64)
	if err != nil || id <= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("id 非法"))
	}
	if err := s.store.SetExchangeLabel(ctx, id, req.Msg.GetLabel()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.SetExchangeLabelResponse{}), nil
}
