package dashboard

import (
	"context"
	"sort"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
	v1 "nox/internal/gen/dashboardv1"
)

// serverGroup D-150 ② 平台分组键=server 首段（首个 '-' 前）。
// 例：Exness-MT5Trial5→Exness、ICMarketsSC-Demo→ICMarketsSC、PxPro-Live→PxPro。
// server 无 '-' → 整段（如 "FxPro MT5 Demo" 含空格无 '-' → 整段为组键）。
func serverGroup(server string) string {
	if i := strings.Index(server, "-"); i > 0 {
		return server[:i]
	}
	return server
}

func (s *Service) ListMatrixUniverse(ctx context.Context, req *connect.Request[v1.ListMatrixUniverseRequest]) (*connect.Response[v1.ListMatrixUniverseResponse], error) {
	accounts, err := s.store.ListBrokerAccounts(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	acctPB := make([]*v1.BrokerAccount, 0, len(accounts))
	for _, a := range accounts {
		ac := &v1.BrokerAccount{
			Venue: a.Venue, Platform: int32(a.Platform), Host: a.Host, Port: int32(a.Port),
			User: a.User, Server: a.Server, Alias: a.Alias, Status: a.Status,
			CreatedAt:   timestamppb.New(a.CreatedAt),
			IsReal:      a.IsReal,              // D-144 ① 三态透出（matrix_rpc 原漏设，D-150 修）
			ServerGroup: serverGroup(a.Server), // D-150 ② 平台分组键后端算好下发
		}
		if a.CommissionBps != nil {
			ac.CommissionBps = a.CommissionBps
		}
		acctPB = append(acctPB, ac)
	}
	allGroups, err := s.bm.AllSymbolGroups(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	allMappings, err := s.store.ListSymbolMappings(ctx, "")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	canonicals, err := s.canonicals(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	universe := buildMatrixUniverse(acctPB, allGroups, allMappings, canonicals)
	return connect.NewResponse(&v1.ListMatrixUniverseResponse{Universe: universe}), nil
}

// canonicals 复用 ListCanonicals 的派生逻辑：生产 alias 表 ∪ 全部已映射 canonical，去重排序（D-066）。
func (s *Service) canonicals(ctx context.Context) ([]string, error) {
	mappings, err := s.store.ListSymbolMappings(ctx, "")
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	for _, c := range allCanonicals() {
		seen[c] = true
	}
	for _, mp := range mappings {
		seen[mp.Canonical] = true
	}
	out := make([]string, 0, len(seen))
	for c := range seen {
		out = append(out, c)
	}
	sort.Strings(out)
	return out, nil
}
