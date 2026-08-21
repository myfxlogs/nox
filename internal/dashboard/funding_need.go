package dashboard

import (
	"time"

	"arb/internal/bus"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// FundingNeedNotice 资金注入提醒事件（D-072）：swap 套利余额不足时发布 → StreamUpdates case 10 → 前端弹窗。
// 不入 alerts 表、不走 alertPaused；断线错过不回放（实时弹窗语义）。
type FundingNeedNotice struct {
	Canonical  string
	VenueLong  string
	VenueShort string
	Reason     string  // SwapArbNotional 返回的 reason（含具体数值）
	NeedUSD    float64 // minNotional − min(equity)；非余额原因（venue 离线/币种不符）= 0
	Ts         time.Time
}

// SetFundingNeedTopic 注入资金注入提醒广播 topic（D-072）。main 创建后注入；nil = 未接线（测试直接构造 Service）。
func (s *Service) SetFundingNeedTopic(t *bus.Topic[FundingNeedNotice]) {
	s.fundingNeedTopic = t
}

// PublishFundingNeed 发布资金注入提醒到 topic（D-072）。topic 未接线时静默丢弃（测试场景）。
func (s *Service) PublishFundingNeed(n FundingNeedNotice) {
	if s.fundingNeedTopic == nil {
		return
	}
	s.fundingNeedTopic.Publish(n)
}

// SwapArbFundingNeedUSD 计算 swap 套利组合需注入资金（D-072）：minNotional − min(equityLong, equityShort)。
// 只读 helper，不改 SwapArbNotional 签名。返回 ok=false 表示 venue 离线/币种不符/报价缺失（非余额原因，NeedUSD 无意义）。
// 仅当 min(equity) < minNotional（余额不足）时返回 ok=true + 正数 NeedUSD。
func (s *Service) SwapArbFundingNeedUSD(canonical, venueLong, venueShort string) (needUSD float64, ok bool) {
	eqLong, curLong, onlineLong := s.AccountEquityUSD(venueLong)
	if !onlineLong {
		return 0, false
	}
	eqShort, curShort, onlineShort := s.AccountEquityUSD(venueShort)
	if !onlineShort {
		return 0, false
	}
	if curLong != "USD" || curShort != "USD" {
		return 0, false
	}
	minEq := eqLong
	if eqShort < minEq {
		minEq = eqShort
	}
	minNotional, okMin := s.SwapMinNotionalUSD(canonical, venueLong, venueShort)
	if !okMin {
		return 0, false
	}
	if minEq >= minNotional {
		return 0, false // 余额充足，无需提醒
	}
	return minNotional - minEq, true
}

// fundingNeedToProto 内部结构 → proto 信封（StreamUpdates case 10）。
func fundingNeedToProto(n FundingNeedNotice) *v1.FundingNeedNotice {
	return &v1.FundingNeedNotice{
		Canonical:  n.Canonical,
		VenueLong:  n.VenueLong,
		VenueShort: n.VenueShort,
		Reason:     n.Reason,
		NeedUsd:    n.NeedUSD,
		Ts:         timestamppb.New(n.Ts),
	}
}
