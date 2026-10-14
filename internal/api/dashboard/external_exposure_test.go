package dashboard

import (
	"testing"
	"time"

	v1 "nox/internal/gen/dashboardv1"
)

// TestD511_ExternalFlag：外单标注——台账查无记录（position_id=0）→ external=true；
// 关联系统仓（索引命中）→ false。判据即台账查无记录，不臆造归属。
func TestD511_ExternalFlag(t *testing.T) {
	bps := []*v1.BrokerPosition{
		{VenueKey: "binance:perp", Ticket: "T1", Symbol: "XAUUSDT", Volume: 0.002}, // 台账无→外单
		{VenueKey: "binance:perp", Ticket: "T2", Symbol: "BTCUSDT", Volume: 0.01},  // 索引命中→系统仓
	}
	annotateWithIndex(bps, map[string]int64{"binance:perp\x00T2": 42}, map[string]string{"binance:perp\x00T2": "o2"})
	if !bps[0].External {
		t.Fatal("台账查无记录行应标 external")
	}
	if bps[1].External {
		t.Fatal("台账关联行不应标 external")
	}
	if !bps[1].IsArb || bps[1].PositionId != 42 {
		t.Fatal("position_id 标注回归")
	}
}

// TestD511_ExternalNotionalNilBPM：bpm 未接线→外单名义 0（fail-open 透视面不拖垮）。
func TestD511_ExternalNotionalNilBPM(t *testing.T) {
	s := &Service{}
	if v := s.externalNotionalUSD(t.Context(), "binance", time.Now()); v != 0 {
		t.Fatalf("nil bpm 应 0，得 %v", v)
	}
}
