package dashboard

// D-008 S1：cexSwapCandidate 纯函数单测（venue 原生 income 窗资格判定）。
// annotateCEXSwap（带 DB）由 TestD375_AnnotateCEXSwap_IncomeSource 集成锚覆盖。

import (
	"testing"
	"time"

	v1 "arb/internal/gen/dashboardv1"

	"google.golang.org/protobuf/types/known/timestamppb"
)

func tsAgo(h time.Duration) *timestamppb.Timestamp {
	return timestamppb.New(time.Now().Add(-h))
}

// TestCEXSwapCandidate_Table 资格表驱动：CEX+venue 未报(0)+OpenedAt 在场=标注候选；
// venue 实报≠0 真值不动（gate pnl_fund）；MT 腿不动；OpenedAt 缺席不标（窗口无界会错价）。
func TestCEXSwapCandidate_Table(t *testing.T) {
	cases := []struct {
		name string
		bp   *v1.BrokerPosition
		want bool
	}{
		{"CEX 未报+有开仓时刻=候选", &v1.BrokerPosition{VenueKey: "binance:perp", Swap: 0, OpenedAt: tsAgo(time.Hour)}, true},
		{"CEX 未报+okx=候选", &v1.BrokerPosition{VenueKey: "okx:perp", Swap: 0, OpenedAt: tsAgo(time.Hour)}, true},
		{"gate 实报≠0 真值不动", &v1.BrokerPosition{VenueKey: "gate:perp", Swap: 0.1209, OpenedAt: tsAgo(time.Hour)}, false},
		{"MT 腿不动", &v1.BrokerPosition{VenueKey: "52993526:mt5", Swap: 4.99, OpenedAt: tsAgo(time.Hour)}, false},
		{"MT 腿 swap=0 也不动", &v1.BrokerPosition{VenueKey: "3001157:mt5", Swap: 0, OpenedAt: tsAgo(time.Hour)}, false},
		{"OpenedAt 缺席不标", &v1.BrokerPosition{VenueKey: "binance:perp", Swap: 0}, false},
		// D-008 S2：外单（PositionId=0）也是候选——venue income 源同享，不再空白。
		{"外单 PositionId=0 也候选", &v1.BrokerPosition{VenueKey: "binance:perp", PositionId: 0, Swap: 0, OpenedAt: tsAgo(2 * time.Hour)}, true},
	}
	for _, c := range cases {
		if got := cexSwapCandidate(c.bp); got != c.want {
			t.Errorf("%s: cexSwapCandidate=%v want %v", c.name, got, c.want)
		}
	}
}
