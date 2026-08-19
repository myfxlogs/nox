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
	rawSymbol    string
	canonical    string
	hasMapping   bool
	contractSize float64
	group        string // 该账号原生符号所属 broker 分组（归并行逐账号保留原生分组）
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
				venue: venue, rawSymbol: sg.Symbol, canonical: canon, hasMapping: mapped, contractSize: sg.ContractSize, group: sg.Group,
			})
		}
	}

	// 已配齐（COMPLETE 且唯一 canonical）行按 canonical 归并：
	// 同商品跨 broker 原生符号不同（XAUUSDm + GOLD → XAUUSD）合并为一行；唯一已配齐行展示名改 canonical、分组保留。
	// 缺 / 部分缺 / 跨 canonical 冲突行保持原生符号行（仍要补映射/修正，D-046）。
	type canonGroup struct {
		canonical string
		rows      []*matrixRowBuilder
	}
	canonByCanonical := make(map[string]*canonGroup)
	canonOrder := make([]string, 0)
	for _, key := range rowOrder {
		row := rowMap[key]
		if row.mappedCount == 0 || row.mappedCount != len(row.venuesSeen) || len(row.canonicals) != 1 {
			continue
		}
		canon := ""
		for c := range row.canonicals {
			canon = c
		}
		g, ok := canonByCanonical[canon]
		if !ok {
			g = &canonGroup{canonical: canon}
			canonByCanonical[canon] = g
			canonOrder = append(canonOrder, canon)
		}
		g.rows = append(g.rows, row)
	}

	// 输出行：原生行（缺/部分缺/冲突） + 唯一已配齐 relabel 行 + canonical 归并行。
	type outRow struct {
		symbol      string
		group       string
		accounts    []matrixAcctInfo
		mapped      int
		total       int
		contracts   map[float64]bool
		canonicals []string // 该行映射到的 canonical 集（原生冲突行可能 >1）
	}
	out := make([]outRow, 0, len(rowOrder)+len(canonOrder))
	mergedCanon := make(map[string]bool)
	for _, key := range rowOrder {
		row := rowMap[key]
		isCanonRow := row.mappedCount > 0 && row.mappedCount == len(row.venuesSeen) && len(row.canonicals) == 1
		if isCanonRow {
			canon := ""
			for c := range row.canonicals {
				canon = c
			}
			g := canonByCanonical[canon]
			if len(g.rows) >= 2 {
				if mergedCanon[canon] {
					continue // 该 canonical 已在首现位置归并，跳过成员原生行
				}
				mergedCanon[canon] = true
				venueSet := make(map[string]bool)
				contracts := make(map[float64]bool)
				for _, r := range g.rows {
					for v := range r.venuesSeen {
						venueSet[v] = true
					}
					for cs := range r.contracts {
						contracts[cs] = true
					}
				}
				mappedAccts := make([]matrixAcctInfo, 0)
				seenVenue := make(map[string]bool)
				for _, r := range g.rows {
					for _, ai := range r.accounts {
						if seenVenue[ai.venue] {
							continue
						}
						seenVenue[ai.venue] = true
						mappedAccts = append(mappedAccts, ai)
					}
				}
				out = append(out, outRow{
					symbol: canon, group: "", accounts: mappedAccts,
					mapped: len(venueSet), total: len(venueSet), contracts: contracts,
					canonicals: []string{canon},
				})
				continue
			}
			// 唯一已配齐行：展示名 = canonical，分组保留（如 EURUSD 仍属 Forex）
			out = append(out, outRow{
				symbol: canon, group: row.group, accounts: row.accounts,
				mapped: row.mappedCount, total: len(row.venuesSeen), contracts: row.contracts,
				canonicals: []string{canon},
			})
			continue
		}
		mappedCanonicals := make([]string, 0, len(row.canonicals))
		for c := range row.canonicals {
			mappedCanonicals = append(mappedCanonicals, c)
		}
		sort.Strings(mappedCanonicals)
		out = append(out, outRow{
			symbol: row.symbol, group: row.group, accounts: row.accounts,
			mapped: row.mappedCount, total: len(row.venuesSeen), contracts: row.contracts,
			canonicals: mappedCanonicals,
		})
	}

	groupOrder := make([]string, 0)
	groupSeen := make(map[string]bool)
	for _, or := range out {
		if !groupSeen[or.group] {
			groupSeen[or.group] = true
			groupOrder = append(groupOrder, or.group)
		}
	}
	groupStats := make(map[string]*v1.MatrixGroupStat, len(groupOrder))
	for _, g := range groupOrder {
		groupStats[g] = &v1.MatrixGroupStat{Group: g}
	}

	symbolRows := make([]*v1.MatrixSymbolRow, 0, len(out))
	for _, or := range out {
		var state v1.MatrixState
		switch {
		case or.total == 0 || or.mapped == or.total:
			state = v1.MatrixState_MATRIX_STATE_COMPLETE
		case or.mapped == 0:
			state = v1.MatrixState_MATRIX_STATE_MISSING
		default:
			state = v1.MatrixState_MATRIX_STATE_PARTIAL
		}
		cMin, cMax := 0.0, 0.0
		if len(or.contracts) > 0 {
			sorted := make([]float64, 0, len(or.contracts))
			for cs := range or.contracts {
				sorted = append(sorted, cs)
			}
			sort.Float64s(sorted)
			cMin, cMax = sorted[0], sorted[len(sorted)-1]
		}
		acctsPB := make([]*v1.MatrixSymbolAccount, 0, len(or.accounts))
		for _, ai := range or.accounts {
			ma := &v1.MatrixSymbolAccount{Venue: ai.venue, RawSymbol: ai.rawSymbol, ContractSize: ai.contractSize, Group: ai.group}
			if ai.hasMapping {
				ma.Canonical = &ai.canonical
			}
			acctsPB = append(acctsPB, ma)
		}
		groupStats[or.group].Total++
		switch state {
		case v1.MatrixState_MATRIX_STATE_COMPLETE:
			groupStats[or.group].Complete++
		case v1.MatrixState_MATRIX_STATE_PARTIAL:
			groupStats[or.group].Partial++
		case v1.MatrixState_MATRIX_STATE_MISSING:
			groupStats[or.group].Missing++
		}
		symbolRows = append(symbolRows, &v1.MatrixSymbolRow{
			Symbol: or.symbol, Group: or.group,
			CoverageTotal: int32(or.total), CoverageMapped: int32(or.mapped), State: state,
			ContractMin: cMin, ContractMax: cMax,
			MappedCanonicals: or.canonicals, Accounts: acctsPB,
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
