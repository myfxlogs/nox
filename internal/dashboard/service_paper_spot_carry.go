// service_paper_spot_carry.go D-352 S1：ListPaperSpotCarry——纸面轨前端卡数据源
// （持仓页第三卡）。三态行源（active+demo+近 14 天 exited）+ join fin_cex_universe 当前
// funding（缺行→null 传播，禁 crash 禁顶替）+ 排序三段 + 15s TTL 缓存。
// 数值全部后端算（days_held/net_now），前端零信任（D-043）。
package dashboard

import (
	"context"
	"math"
	"sort"
	"time"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"arb/internal/store/pgstore"

	"connectrpc.com/connect"
)

// paperBoardTTL 纸面卡缓存 TTL（financeOverviewTTL/carryLiveTTL 同款 15s——聚合查询防
// 热路径重压；上游 4h 步进，15s 陈旧窗足可接受）。
const paperBoardTTL = 15 * time.Second

// paperSpotCostPct net_now 口径常量：|last_funding|−成本（与 scan.SPOT_CARRY_COST_PCT 同值；
// dashboard 不 import scan（跨域依赖），数值以注释锚定同步）。
const paperSpotCostPct = 0.15

// paperExitRecentWindow exited 行证据流窗口（提示词 §1：近 14 天）。
const paperExitRecentWindow = 14 * 24 * time.Hour

// paperTurnCostPct D-354 全周转成本预估常量：现货 taker 0.1%×2+perp taker 0.05%×2=0.30%
// （公开费率卡常量非实测，诚实标注）。与 net_now 的 0.15%/yr 摊销口径并存不混淆：
// net_now=排名展示年化扣减；0.30%=平仓一次性实际成本预估。
const paperTurnCostPct = 0.003

// paperBoardSource 数据源窄接口（*pgstore.Store 原生满足；单测 fake 注入驱动 TTL/排序锚）。
type paperBoardSource interface {
	ListPaperSpotCarryBoard(ctx context.Context, recentWindow time.Duration) ([]pgstore.PaperSpotCarry, error)
	ListCexUniverse(ctx context.Context) (map[string]pgstore.CexUniverseRow, error)
}

// ListPaperSpotCarry 纸面轨三态清单（15s TTL 缓存，语义同 GetFinanceOverview）。
func (s *Service) ListPaperSpotCarry(ctx context.Context, req *connect.Request[v1.ListPaperSpotCarryRequest]) (*connect.Response[v1.ListPaperSpotCarryResponse], error) {
	s.paperBoardMu.Lock()
	if s.paperBoardCached != nil && time.Since(s.paperBoardCachedAt) < paperBoardTTL {
		resp := s.paperBoardCached
		s.paperBoardMu.Unlock()
		return connect.NewResponse(resp), nil
	}
	s.paperBoardMu.Unlock()

	src := s.paperBoardSourceOf()
	rows, err := buildPaperSpotCarryBoard(ctx, src, time.Now())
	if err != nil {
		return nil, err
	}
	resp := &v1.ListPaperSpotCarryResponse{Rows: rows}

	s.paperBoardMu.Lock()
	s.paperBoardCached, s.paperBoardCachedAt = resp, time.Now()
	s.paperBoardMu.Unlock()
	return connect.NewResponse(resp), nil
}

// paperBoardSourceOf 生产=s.store；单测注入 fake 时走注入（nil 缺省回退生产源）。
func (s *Service) paperBoardSourceOf() paperBoardSource {
	if s.paperBoardSrc != nil {
		return s.paperBoardSrc
	}
	return s.store
}

// buildPaperSpotCarryBoard 行构建+三段排序（纯函数，注入 now 供数值锚测试）。
func buildPaperSpotCarryBoard(ctx context.Context, src paperBoardSource, now time.Time) ([]*v1.PaperSpotCarryRow, error) {
	papers, err := src.ListPaperSpotCarryBoard(ctx, paperExitRecentWindow)
	if err != nil {
		return nil, err
	}
	uni, err := src.ListCexUniverse(ctx)
	if err != nil {
		return nil, err
	}

	rows := make([]*v1.PaperSpotCarryRow, 0, len(papers))
	for _, p := range papers {
		row := &v1.PaperSpotCarryRow{
			Id:              p.ID,
			Venue:           p.Venue,
			Symbol:          p.Symbol,
			Side:            p.Side,
			Status:          p.Status,
			QtyUsd:          p.QtyUSD,
			EntryFundingAnn: p.EntryFundingAnn,
			AccruedUsd:      p.AccruedUSD,
			EntryTs:         p.EntryTs.Format(time.RFC3339),
			DaysHeld:        math.Floor(now.Sub(p.EntryTs).Hours()/24*10) / 10,
			Note:            p.Note,
		}
		if p.ExitTs != nil {
			row.ExitTs = p.ExitTs.Format(time.RFC3339)
		}
		row.ExitReason = p.ExitReason
		// join fin_cex_universe（venue|symbol 键，与纸面步进同款）；缺行→null（fail-closed 传播）
		if u, ok := uni[p.Venue+"|"+p.Symbol]; ok && u.FundingAnn != nil {
			last := *u.FundingAnn
			row.LastFundingAnn = &last
			net := math.Abs(last) - paperSpotCostPct
			row.NetNow = &net
		}
		// D-354 S3：盈亏三数——基差浮盈（short: qty×(entry−last)/1e4；long 反号）；
		// 净浮盈=accrued+basis−qty×0.003；两列基差 NULL→basis/total NULL（诚实「—」）。
		row.EntryBasisBps = p.EntryBasisBps
		row.LastBasisBps = p.LastBasisBps
		if p.EntryBasisBps != nil && p.LastBasisBps != nil {
			diff := (*p.EntryBasisBps - *p.LastBasisBps) / 1e4
			if p.Side == "long_perp" {
				diff = -diff
			}
			basis := p.QtyUSD * diff
			row.BasisPnlUsd = &basis
			total := p.AccruedUSD + basis - p.QtyUSD*paperTurnCostPct
			row.TotalPnlUsd = &total
		}
		// 三率：累计收益率%；实收年化%（days=days_held 同源值，0 天护→null）
		row.RetPct = p.AccruedUSD / p.QtyUSD * 100
		if days := row.DaysHeld; days > 0 {
			ann := p.AccruedUSD / p.QtyUSD * 365 / days * 100
			row.RealizedAnnPct = &ann
		}
		rows = append(rows, row)
	}

	// 排序三段：active（net_now desc，null 殿后）→ demo → exited（exit_ts desc）；
	// 段内其余键保持稳定（sort.SliceStable，demo 段=在轨时长自然序）。
	sort.SliceStable(rows, func(i, j int) bool {
		ri, rj := segRank(rows[i]), segRank(rows[j])
		if ri != rj {
			return ri < rj
		}
		switch ri {
		case 0: // active：net desc，null 殿后（null=-Inf 排尾）
			ni, nj := netOrNegInf(rows[i]), netOrNegInf(rows[j])
			if ni != nj {
				return ni > nj
			}
		case 2: // exited：新退出的在前
			ti, _ := time.Parse(time.RFC3339, rows[i].ExitTs)
			tj, _ := time.Parse(time.RFC3339, rows[j].ExitTs)
			return ti.After(tj)
		}
		return false
	})
	return rows, nil
}

// segRank 三段序：active=0 / demo=1 / exited=2。
func segRank(r *v1.PaperSpotCarryRow) int {
	switch r.Status {
	case "active":
		return 0
	case "demo":
		return 1
	default:
		return 2
	}
}

func netOrNegInf(r *v1.PaperSpotCarryRow) float64 {
	if r.NetNow == nil {
		return math.Inf(-1)
	}
	return *r.NetNow
}
