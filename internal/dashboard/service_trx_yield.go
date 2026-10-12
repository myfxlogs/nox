// D-278 S1/S4：净收益率日曲线 RPC handler（GetTrxYieldCurve）。
// 三成分日序列（energy/voting TRX + overnight USD）+ 7d/30d 滚动年化 + S4 敞口计量。
// 后端算好，前端零计算（D-043 前端零信任）。分母=$36.5K 池口径常量（AF4）。
package dashboard

import (
	"context"
	"math"
	"time"

	v1 "arb/internal/gen/dashboardv1"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// trxPoolPrincipalUSD D-278 S1：年化分母=$36.5K 池口径常量（与底稿可比，AF4）。
const trxPoolPrincipalUSD = 36500.0

// trxShortInstrument D-278 S1：TRX 空单 instrument ID（X-AFQUL，单空单事实）。
const trxShortInstrument = "X-AFQUL"

// GetTrxYieldCurve D-278 S1：净收益率日曲线——三成分日序列+滚动年化+S4 敞口。
// 冷启动（无数据）→ 全零响应（前端「数据未就绪」），不猜不报错。
func (s *Service) GetTrxYieldCurve(ctx context.Context, req *connect.Request[v1.GetTrxYieldCurveRequest]) (*connect.Response[v1.GetTrxYieldCurveResponse], error) {
	days := int(req.Msg.Days)
	if days <= 0 {
		days = 30
	}
	now := time.Now().UTC()
	from := now.AddDate(0, 0, -days)

	// 1. 日级三成分序列
	yieldDays, err := s.store.ListTRXYieldByDay(ctx, from, now)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// 2. 现价（fin_cmc_prices REST mid，X-AFQUL——D-278 F2 换源，复用 D-208 既有方法）
	var currentPrice float64
	if mid, _, err := s.store.LastCMCPriceMidWithTime(ctx, trxShortInstrument); err == nil {
		currentPrice = mid
	}

	// 3. 构造日序列 + 滚动年化
	points := make([]*v1.TrxYieldCurvePoint, 0, len(yieldDays))
	var sum7d, sum30d float64
	var count7d, count30d int
	for _, yd := range yieldDays {
		price := currentPrice // 回退用现价（历史日无报价时）
		if yd.Price != nil {
			price = *yd.Price
		}
		dailyTotalUSD := (yd.EnergyTrx+yd.VotingTrx)*price + yd.InterestUSD
		pt := &v1.TrxYieldCurvePoint{
			Day:           timestamppb.New(yd.Day),
			EnergyTrx:     yd.EnergyTrx,
			VotingTrx:     yd.VotingTrx,
			InterestUsd:   yd.InterestUSD,
			DailyTotalUsd: dailyTotalUSD,
		}
		if yd.Price != nil {
			pt.Price = *yd.Price
		}
		points = append(points, pt)

		// 滚动窗口累计（按 day ASC，最后 N 天）
		ageDays := int(now.Sub(yd.Day) / (24 * time.Hour))
		if ageDays < 7 {
			sum7d += dailyTotalUSD
			count7d++
		}
		if ageDays < 30 {
			sum30d += dailyTotalUSD
			count30d++
		}
	}

	// 4. 滚动年化 = 窗口合计 USD / $36.5K × 365 / 窗口天数
	var ann7d, ann30d float64
	if count7d > 0 {
		ann7d = (sum7d / trxPoolPrincipalUSD) * 365.0 / float64(count7d) * 100
	}
	if count30d > 0 {
		ann30d = (sum30d / trxPoolPrincipalUSD) * 365.0 / float64(count30d) * 100
	}

	// 5. S4：累积未对冲 TRX 收益（rental + vote_claim 全量累计）
	rentalSun, _ := s.store.SumTRXTxRentalAll(ctx)
	voteSun, _ := s.store.SumTRXTxVoteClaimAll(ctx)
	cumIncomeTRX := float64(rentalSun+voteSun) / 1e6
	cumIncomeUSD := cumIncomeTRX * currentPrice

	// 6. S4：对冲比例漂移（现货含奖励 vs 空单名义）
	var driftPct float64
	var driftWarn bool
	if trxAcc, err := s.store.LastTRXAccount(ctx); err == nil && currentPrice > 0 {
		balance := float64(trxAcc.BalanceSun) / 1e6
		staked := float64(trxAcc.TronPower) / 1e6
		spotExposure := balance + staked // D-295：禁加累计收入——claim 入 balance 后已被转出（DB 实证 balance=29.5 ≪ claims 1238），
		// 加回=幽灵敞口，漂移虚高 ~1.3% 且随 claim 线性累积 ~2 个月后假穿 2% 告警线
		if pos, err := s.store.LastCMCPositionByInstrument(ctx, trxShortInstrument); err == nil && pos.Quantity > 0 {
			shortNotional := pos.Quantity
			driftPct = math.Abs(spotExposure-shortNotional) / shortNotional * 100
			driftWarn = driftPct > 2.0
		}
	}

	resp := &v1.GetTrxYieldCurveResponse{
		Points:                      points,
		Annualized_7D:               ann7d,
		Annualized_30D:              ann30d,
		CurrentPrice:                currentPrice,
		PoolPrincipalUsd:            trxPoolPrincipalUSD,
		SampleDays_7D:               int32(count7d),
		SampleDays_30D:              int32(count30d),
		CumulativeUnhedgedIncomeTrx: cumIncomeTRX,
		CumulativeUnhedgedIncomeUsd: cumIncomeUSD,
		HedgeRatioDriftPct:          driftPct,
		HedgeRatioDriftWarn:         driftWarn,
	}
	return connect.NewResponse(resp), nil
}
