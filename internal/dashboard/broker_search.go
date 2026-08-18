// 经纪商搜索：通过 mtapi.io gRPC 网关按公司名查找 MT4/MT5 服务器（docs/broker管理器实现方案.md §3）。
package dashboard

import (
	"context"
	"crypto/tls"
	"fmt"
	"strings"
	"time"

	"arb/internal/adapter/gen/mtapi/mt4"
	"arb/internal/adapter/gen/mtapi/mt5"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"connectrpc.com/connect"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// mtapi 网关地址：与 internal/adapter 内现有适配器常量一致（MT4 实测 mt4grpc / MT5 mt5grpc3）。
const (
	mt4Gateway = "mt4grpc.mtapi.io:443"
	mt5Gateway = "mt5grpc3.mtapi.io:443"
)

// SearchBroker 按平台 + 公司关键词搜索经纪商服务器。错误经 connect error 返回。
func (s *Service) SearchBroker(ctx context.Context, req *connect.Request[v1.SearchBrokerRequest]) (*connect.Response[v1.SearchBrokerResponse], error) {
	company := strings.TrimSpace(req.Msg.GetCompany())
	if company == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("请输入经纪商名称"))
	}
	var companies []*v1.BrokerCompany
	var err error
	switch req.Msg.GetPlatform() {
	case 0:
		companies, err = searchMT4(ctx, company)
	case 1:
		companies, err = searchMT5(ctx, company)
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("未知平台类型: %d", req.Msg.GetPlatform()))
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return connect.NewResponse(&v1.SearchBrokerResponse{Companies: companies}), nil
}

// dialMT 临时拨号 mtapi 网关。搜索频率低，即查即断，不持有长连接。
func dialMT(ctx context.Context, gateway string) (*grpc.ClientConn, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return grpc.DialContext(ctx, gateway,
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})),
	)
}

func searchMT4(ctx context.Context, company string) ([]*v1.BrokerCompany, error) {
	conn, err := dialMT(ctx, mt4Gateway)
	if err != nil {
		return nil, fmt.Errorf("连接 MT4 网关失败: %w", err)
	}
	defer conn.Close()

	reply, err := mt4.NewServiceClient(conn).Search(ctx, &mt4.SearchRequest{Company: company})
	if err != nil {
		return nil, fmt.Errorf("MT4 搜索失败: %w", err)
	}
	if e := reply.GetError(); e != nil {
		return nil, fmt.Errorf("MT4 搜索错误: %s", e.GetMessage())
	}
	companies := make([]*v1.BrokerCompany, 0, len(reply.GetResult()))
	for _, c := range reply.GetResult() {
		servers := make([]*v1.BrokerServer, 0, len(c.GetResults()))
		for _, r := range c.GetResults() {
			servers = append(servers, &v1.BrokerServer{Name: r.GetName(), Access: firstAccess(r.GetAccess(), false)})
		}
		companies = append(companies, &v1.BrokerCompany{CompanyName: c.GetCompanyName(), Servers: servers})
	}
	return companies, nil
}

func searchMT5(ctx context.Context, company string) ([]*v1.BrokerCompany, error) {
	conn, err := dialMT(ctx, mt5Gateway)
	if err != nil {
		return nil, fmt.Errorf("连接 MT5 网关失败: %w", err)
	}
	defer conn.Close()

	reply, err := mt5.NewServiceClient(conn).Search(ctx, &mt5.SearchRequest{Company: company})
	if err != nil {
		return nil, fmt.Errorf("MT5 搜索失败: %w", err)
	}
	if e := reply.GetError(); e != nil {
		return nil, fmt.Errorf("MT5 搜索错误: %s", e.GetMessage())
	}
	companies := make([]*v1.BrokerCompany, 0, len(reply.GetResult()))
	for _, c := range reply.GetResult() {
		servers := make([]*v1.BrokerServer, 0, len(c.GetResults()))
		for _, r := range c.GetResults() {
			servers = append(servers, &v1.BrokerServer{Name: r.GetName(), Access: firstAccess(r.GetAccess(), true)})
		}
		companies = append(companies, &v1.BrokerCompany{CompanyName: c.GetCompanyName(), Servers: servers})
	}
	return companies, nil
}

// firstAccess 提取 access 端点。multiAccess（MT5）时逗号拼接全部 failover 端点，由前端解析首端点。
func firstAccess(access []string, multiAccess bool) string {
	if len(access) == 0 {
		return ""
	}
	if multiAccess {
		return strings.Join(access, ", ")
	}
	return access[0]
}
