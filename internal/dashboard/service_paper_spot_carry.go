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

	v1 "arb/internal/gen/dashboardv1"
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
// 热修 hotfix-paper：另有 pgstore.paperExitRecentLimit=500 硬上限（OOM pprof 实证）。
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
	resp, err := buildPaperSpotCarryBoard(ctx, src, time.Now())
	if err != nil {
		return nil, err
	}

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

// buildPaperSpotCarryBoard 行构建+三段排序+轨级汇总（纯函数，注入 now 供数值锚测试）。
// D-393：tracks 由同一批 papers 聚合（零新查询）；days_held 存活口径=(exit_ts ?? now)−entry_ts
// ——exited 行旧口径 now−entry_ts 是活计时器（退出后数字继续涨，DB 实拍可复现），随票收编。
func buildPaperSpotCarryBoard(ctx context.Context, src paperBoardSource, now time.Time) (*v1.ListPaperSpotCarryResponse, error) {
	papers, err := src.ListPaperSpotCarryBoard(ctx, paperExitRecentWindow)
	if err != nil {
		return nil, err
	}
	uni, err := src.ListCexUniverse(ctx)
	if err != nil {
		return nil, err
	}

	rows := make([]*v1.PaperSpotCarryRow, 0, len(papers))
	tracks := map[string]*v1.PaperTrackSummary{}
	for _, p := range papers {
		structure := p.Structure
		if structure == "" {
			structure = "spot_carry" // 历史行空串归一（scanPaperSpotCarry 同款）
		}
		// 存活时长：exited 冻结在 exit_ts，active/demo 走到 now（退出后不再涨）。
		heldEnd := now
		if p.ExitTs != nil {
			heldEnd = *p.ExitTs
		}
		// 轨级聚合（win 判定 total_pnl_usd 优先、accrued_usd 兜底——见 proto 口径钉死）。
		tr := tracks[structure]
		if tr == nil {
			tr = &v1.PaperTrackSummary{Structure: structure}
			tracks[structure] = tr
		}
		row := &v1.PaperSpotCarryRow{
			Id:              p.ID,
			Venue:           p.VenuePair(),
			Symbol:          p.Symbol,
			Side:            p.Side,
			Status:          p.Status,
			QtyUsd:          p.QtyUSD,
			EntryFundingAnn: p.EntryFundingAnn,
			AccruedUsd:      p.AccruedUSD,
			EntryTs:         p.EntryTs.Format(time.RFC3339),
			DaysHeld:        math.Floor(heldEnd.Sub(p.EntryTs).Hours()/24*10) / 10,
			Note:            p.Note,
			Structure:       structure,
			MtVenue:         p.MtVenue,
			// D-474b S1：est_fee 透出——perp_perp 存量行（NULL）前端徽标「观测轨」。
			EstRoundTripFeeUsd: p.EstRoundTripFeeUSD,
		}
		if p.ExitTs != nil {
			row.ExitTs = p.ExitTs.Format(time.RFC3339)
		}
		row.ExitReason = p.ExitReason
		// join fin_cex_universe（venue|symbol 键，与纸面步进同款）；缺行→null（fail-closed 传播）
		if u, ok := uni[p.VenuePair()+"|"+p.Symbol]; ok && u.FundingAnn != nil {
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
			// D-410 S6：展示与结算同口径——dir×(last−entry)（原 entry−last 对
			// long_perp/B 结构行符号反向）。dir：空腿=基差被减项→−1。
			dir := -1.0
			if p.Side == "long_perp" {
				dir = 1
			}
			basis := p.QtyUSD * dir * (*p.LastBasisBps - *p.EntryBasisBps) / 1e4
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
		switch p.Status {
		case "active":
			tr.ActiveN++
			tr.AccruedHoldingUsd += p.AccruedUSD
			tr.NotionalHoldingUsd += p.QtyUSD
		case "demo":
			tr.DemoN++
			tr.AccruedHoldingUsd += p.AccruedUSD
			tr.NotionalHoldingUsd += p.QtyUSD
		default: // exited（窗口内）
			tr.ExitedN++
			tr.ExitedAccruedSumUsd += p.AccruedUSD
			win := p.AccruedUSD // total_pnl 优先，NULL 回退 accrued（基差缺失时收割口径兜底）
			if row.TotalPnlUsd != nil {
				win = *row.TotalPnlUsd
			}
			if win > 0 {
				tr.WinRatePct++ // 暂存 win 计数，循环后换算%
			}
			tr.AvgExitDays += heldEnd.Sub(p.EntryTs).Hours() / 24 // 暂存合计，循环后取均
		}
		rows = append(rows, row)
	}
	for _, tr := range tracks {
		if tr.ExitedN > 0 {
			tr.WinRatePct = tr.WinRatePct / float64(tr.ExitedN) * 100
			tr.AvgExitDays /= float64(tr.ExitedN)
		}
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

	// 轨序固定（四轨），未知 structure 尾部字典序追加（未来新轨不丢）。
	order := map[string]int{"spot_carry": 0, "perp_perp": 1, "long_cfd_short_perp": 2, "short_cfd_long_perp": 3}
	trackList := make([]*v1.PaperTrackSummary, 0, len(tracks))
	for _, tr := range tracks {
		trackList = append(trackList, tr)
	}
	sort.SliceStable(trackList, func(i, j int) bool {
		oi, oki := order[trackList[i].Structure]
		oj, okj := order[trackList[j].Structure]
		if oki != okj {
			return oki // 已知轨在前
		}
		if oki {
			return oi < oj
		}
		return trackList[i].Structure < trackList[j].Structure
	})
	return &v1.ListPaperSpotCarryResponse{Rows: rows, Tracks: trackList}, nil
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
