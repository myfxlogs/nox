package dashboard

import (
	"nox/internal/data/bus"

	v1 "nox/internal/gen/dashboardv1"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// FundingNeedNotice 已迁 internal/bus（D-508 S7b：autotrade 窄接口解逆向依赖，
// 事件类型居中立叶子包；别名填充语义不变——D-154 返修② aliasOrKey 后端权威）。
type FundingNeedNotice = bus.FundingNeedNotice // 别名兼容存量引用面

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

// SwapArbFundingNeedUSD 计算 swap 套利组合需注入资金（D-072；D-097 R2 sizing 口径同步；D-174 杠杆感知）。
// 只读 helper，不改 SwapArbNotional 签名。返回 ok=false 表示 venue 离线/币种不符/报价缺失（非余额原因，NeedUSD 无意义）。
// D-174：保证金口径——maxNotional=minEq×ratio×leverage，不足时 NeedUSD=保证金差额=需注入权益。
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
	// D-174：保证金口径——maxNotional=minEq×ratio×leverage，不足时 NeedUSD=保证金差额=需注入权益。
	ratio := swapArbRatio(s.swapArbEquityUseRatio)
	longLev, _ := s.AccountLeverage(venueLong)
	shortLev, _ := s.AccountLeverage(venueShort)
	leverage := effectiveLeverage(longLev, shortLev)
	maxNotional := minEq * ratio * leverage
	if maxNotional >= minNotional {
		return 0, false // sizing 充足，无需提醒
	}
	// NeedUSD = 保证金差额 = 需注入权益
	requiredMargin := minNotional / leverage
	availableMargin := minEq * ratio
	return requiredMargin - availableMargin, true
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
		AliasLong:  n.AliasLong,
		AliasShort: n.AliasShort,
	}
}
