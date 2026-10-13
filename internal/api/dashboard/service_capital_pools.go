// service_capital_pools.go —— 资金地图聚合 RPC（业主令 10-13 + D-285）：
// 池快照（fin_venue_account_snapshot）× 在跑归因（OPEN 腿名义 / 未到期 DCI /
// earn 池全额 / MT 占用保证金），按币种分组零汇率换算（USDT 即 USDT、USD 即 USD）。
package dashboard

import (
	"context"
	"fmt"
	"sort"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
	v1 "nox/internal/gen/dashboardv1"
	"nox/internal/store/pgstore"
)

// capitalPoolStaleAfter 池快照陈旧阈值：与 combo 判定「池快照过期 >5min」同窗；
// scheduler 源（日采钱包面）放宽至 36h（产源节奏+宽限，不硬套事件源节奏）。
const capitalPoolStaleAfter = 5 * time.Minute
const capitalSchedulerStaleAfter = 36 * time.Hour

func capitalPoolStale(src string, fetchedAt, now time.Time) bool {
	if src == "scheduler" {
		return now.Sub(fetchedAt) > capitalSchedulerStaleAfter
	}
	return now.Sub(fetchedAt) > capitalPoolStaleAfter
}

// GetCapitalPools 资金地图：每池 权益/闲置/在跑 归因，币种分组后端算。
func (s *Service) GetCapitalPools(ctx context.Context, req *connect.Request[v1.GetCapitalPoolsRequest]) (*connect.Response[v1.GetCapitalPoolsResponse], error) {
	pools, err := s.store.CapitalPoolSnapshots(ctx)
	if err != nil {
		return nil, err
	}
	legs, err := s.store.CapitalOpenLegs(ctx)
	if err != nil {
		return nil, err
	}
	dcis, err := s.store.CapitalOutstandingDCI(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	resp := s.buildCapitalPools(pools, legs, dcis, now)
	return connect.NewResponse(resp), nil
}

// buildCapitalPools 纯函数组装（测试锚直调）。
func (s *Service) buildCapitalPools(pools []pgstore.CapitalPoolRow, legs []pgstore.CapitalDeployedLeg,
	dcis []pgstore.CapitalDCIOrder, now time.Time) *v1.GetCapitalPoolsResponse {

	legByPool := map[string][]pgstore.CapitalDeployedLeg{}
	for _, l := range legs {
		legByPool[l.VenueKey] = append(legByPool[l.VenueKey], l)
	}
	dciByVenue := map[string][]pgstore.CapitalDCIOrder{}
	for _, o := range dcis {
		dciByVenue[o.Venue] = append(dciByVenue[o.Venue], o)
	}
	// DCI 锁额归属：申购自现货钱包扣款——venue 有 spot 池→spot；无 spot 有
	// funding 池→funding；皆无→不归因（锁额不落虚池）。
	dciHome := map[string]string{}
	for _, row := range pools {
		if _, ok := dciHome[row.Venue]; !ok && (row.VenueType == "spot" || row.VenueType == "funding") {
			dciHome[row.Venue] = row.VenueType
		}
		if row.VenueType == "spot" {
			dciHome[row.Venue] = "spot"
		}
	}

	groups := map[string]*v1.CapitalPoolsGroup{}
	for _, row := range pools {
		p := &v1.CapitalPool{
			Venue: row.Venue, VenueType: row.VenueType, Currency: row.Currency,
			Equity: row.Equity, Ghost: row.Ghost,
			UpdatedAt: timestamppb.New(row.FetchedAt),
		}
		p.Stale = capitalPoolStale(row.Src, row.FetchedAt, now)
		p.Online = !p.Stale && row.Src != ""
		poolKey := row.Venue + ":" + row.VenueType
		inPool := 0.0 // 池内在跑（占着 equity）；池外锁额（DCI）只入 Deployed 不动 Idle
		switch {
		case row.VenueType == "mt4" || row.VenueType == "mt5":
			// MT 池在跑=占用保证金（equity−free_margin）。
			if dep := row.Equity - row.FreeMargin; dep > 0 {
				inPool = dep
				p.DeployedParts = append(p.DeployedParts, &v1.CapitalDeployedPart{
					Kind: "margin", Ref: "占用保证金", Amount: dep,
				})
			}
		case row.VenueType == "earn":
			// earn 池全额即在跑（活期理财仓，D-285 裁定口径）。
			inPool = row.Equity
			if row.Equity > 0 {
				p.DeployedParts = append(p.DeployedParts, &v1.CapitalDeployedPart{
					Kind: "earn", Ref: fmt.Sprintf("%s earn", row.Venue), Amount: row.Equity,
				})
			}
		default:
			for _, l := range legByPool[poolKey] {
				if l.Amount <= 0 {
					continue
				}
				inPool += l.Amount
				p.DeployedParts = append(p.DeployedParts, &v1.CapitalDeployedPart{
					Kind:   "position",
					Ref:    fmt.Sprintf("%s #%d", l.PosType, l.PositionID),
					Amount: l.Amount,
				})
			}
			if dciHome[row.Venue] == row.VenueType {
				for _, o := range dciByVenue[row.Venue] {
					if o.CCY != row.Currency {
						continue
					}
					// 池外在跑：DCI 锁额已离池（快照 equity=扣款后余额）——
					// 入 Deployed 不计 inPool，idle 不双减（终审 G3）。
					p.Deployed += o.Qty
					p.DeployedParts = append(p.DeployedParts, &v1.CapitalDeployedPart{
						Kind: "dci", Ref: fmt.Sprintf("DCI#%d（池外锁额）", o.ID), Amount: o.Qty,
					})
				}
			}
		}
		p.Deployed += inPool
		p.Idle = p.Equity - inPool

		g := groups[p.Currency]
		if g == nil {
			g = &v1.CapitalPoolsGroup{Currency: p.Currency}
			groups[p.Currency] = g
		}
		g.Pools = append(g.Pools, p)
		if !p.Ghost {
			g.Equity += p.Equity
			g.Deployed += p.Deployed
			g.Idle += p.Idle
		}
	}

	ccys := make([]string, 0, len(groups))
	for c := range groups {
		ccys = append(ccys, c)
	}
	sort.Strings(ccys)
	resp := &v1.GetCapitalPoolsResponse{ComputedAt: timestamppb.New(now)}
	for _, c := range ccys {
		g := groups[c]
		sort.Slice(g.Pools, func(i, j int) bool {
			if g.Pools[i].Venue != g.Pools[j].Venue {
				return g.Pools[i].Venue < g.Pools[j].Venue
			}
			return g.Pools[i].VenueType < g.Pools[j].VenueType
		})
		resp.Groups = append(resp.Groups, g)
	}
	return resp
}
