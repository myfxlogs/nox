package dashboard

import (
	"context"
	"sort"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

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
			CreatedAt: timestamppb.New(a.CreatedAt),
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
