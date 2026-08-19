package dashboard

import (
	"context"
	"sort"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"arb/internal/store/pgstore"
	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type matrixRowBuilder struct {
	symbol      string
	group       string
	venuesSeen  map[string]bool
	canonicals  map[string]bool
	contracts   map[float64]bool
	mappedCount int
	accounts    []matrixAcctInfo
}

type matrixAcctInfo struct {
	venue        string
	canonical    string
	hasMapping   bool
	contractSize float64
}

// buildMatrixUniverse 把聚合数据装配为 MatrixUniverse（纯函数，便于单测 D-045 §8.1）。
func buildMatrixUniverse(
	accounts []*v1.BrokerAccount,
	allGroups map[string][]SymbolGroup,
	mappings []pgstore.SymbolMapping,
	canonicals []string,
) *v1.MatrixUniverse {
	type mapKey struct{ venue, symbol string }
	mapIdx := make(map[mapKey]string, len(mappings))
	for _, mp := range mappings {
		mapIdx[mapKey{mp.Venue, mp.BrokerSymbol}] = mp.Canonical
	}

	connected := make([]string, 0, len(accounts))
	for _, a := range accounts {
		if a.Status == "connected" {
			connected = append(connected, a.Venue)
		}
	}

	type rowKey struct{ symbol, group string }
	rowMap := make(map[rowKey]*matrixRowBuilder)
	rowOrder := make([]rowKey, 0)
	for _, venue := range connected {
		groups, ok := allGroups[venue]
		if !ok {
			continue
		}
		for _, sg := range groups {
			key := rowKey{sg.Symbol, sg.Group}
			row, exists := rowMap[key]
			if !exists {
				row = &matrixRowBuilder{
					symbol: sg.Symbol, group: sg.Group,
					venuesSeen: make(map[string]bool), canonicals: make(map[string]bool),
					contracts: make(map[float64]bool),
				}
				rowMap[key] = row
				rowOrder = append(rowOrder, key)
			}
			row.venuesSeen[venue] = true
			if cs := sg.ContractSize; cs > 0 {
				row.contracts[cs] = true
			}
			canon, mapped := mapIdx[mapKey{venue, sg.Symbol}]
			if mapped {
				row.canonicals[canon] = true
				row.mappedCount++
			}
			row.accounts = append(row.accounts, matrixAcctInfo{
				venue: venue, canonical: canon, hasMapping: mapped, contractSize: sg.ContractSize,
			})
		}
	}

	groupOrder := make([]string, 0)
	groupSeen := make(map[string]bool)
	for _, key := range rowOrder {
		g := key.group
		if !groupSeen[g] {
			groupSeen[g] = true
			groupOrder = append(groupOrder, g)
		}
	}
	groupStats := make(map[string]*v1.MatrixGroupStat, len(groupOrder))
	for _, g := range groupOrder {
		groupStats[g] = &v1.MatrixGroupStat{Group: g}
	}

	symbolRows := make([]*v1.MatrixSymbolRow, 0, len(rowOrder))
	for _, key := range rowOrder {
		row := rowMap[key]
		total := len(row.venuesSeen)
		mapped := row.mappedCount
		var state v1.MatrixState
		switch {
		case total == 0 || mapped == total:
			state = v1.MatrixState_MATRIX_STATE_COMPLETE
		case mapped == 0:
			state = v1.MatrixState_MATRIX_STATE_MISSING
		default:
			state = v1.MatrixState_MATRIX_STATE_PARTIAL
		}
		cMin, cMax := 0.0, 0.0
		if len(row.contracts) > 0 {
			sorted := make([]float64, 0, len(row.contracts))
			for cs := range row.contracts {
				sorted = append(sorted, cs)
			}
			sort.Float64s(sorted)
			cMin, cMax = sorted[0], sorted[len(sorted)-1]
		}
		mappedCanonicals := make([]string, 0, len(row.canonicals))
		for c := range row.canonicals {
			mappedCanonicals = append(mappedCanonicals, c)
		}
		sort.Strings(mappedCanonicals)
		acctsPB := make([]*v1.MatrixSymbolAccount, 0, len(row.accounts))
		for _, ai := range row.accounts {
			ma := &v1.MatrixSymbolAccount{Venue: ai.venue, ContractSize: ai.contractSize}
			if ai.hasMapping {
				ma.Canonical = &ai.canonical
			}
			acctsPB = append(acctsPB, ma)
		}
		groupStats[key.group].Total++
		switch state {
		case v1.MatrixState_MATRIX_STATE_COMPLETE:
			groupStats[key.group].Complete++
		case v1.MatrixState_MATRIX_STATE_PARTIAL:
			groupStats[key.group].Partial++
		case v1.MatrixState_MATRIX_STATE_MISSING:
			groupStats[key.group].Missing++
		}
		symbolRows = append(symbolRows, &v1.MatrixSymbolRow{
			Symbol: row.symbol, Group: row.group,
			CoverageTotal: int32(total), CoverageMapped: int32(mapped), State: state,
			ContractMin: cMin, ContractMax: cMax,
			MappedCanonicals: mappedCanonicals, Accounts: acctsPB,
		})
	}
	groupStatList := make([]*v1.MatrixGroupStat, 0, len(groupOrder))
	for _, g := range groupOrder {
		groupStatList = append(groupStatList, groupStats[g])
	}
	return &v1.MatrixUniverse{
		Groups:     groupOrder,
		GroupStats: groupStatList,
		Symbols:    symbolRows,
		Accounts:   accounts,
		Canonicals: canonicals,
	}
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

// canonicals 复用 ListCanonicals 的派生逻辑：默认种子 ∪ 全部已映射 canonical，去重排序。
func (s *Service) canonicals(ctx context.Context) ([]string, error) {
	mappings, err := s.store.ListSymbolMappings(ctx, "")
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	for _, c := range defaultSymbols {
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
