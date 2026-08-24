package dashboard

import (
	"testing"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
)

// TestAnnotateWithIndex_Hit 命中：broker ticket 在索引中 → position_id 标注。
func TestAnnotateWithIndex_Hit(t *testing.T) {
	bps := []*v1.BrokerPosition{
		{VenueKey: "52993526:mt5", Ticket: "1001"},
		{VenueKey: "277842155:mt5", Ticket: "2002"},
	}
	idx := map[string]int64{
		"52993526:mt5\x001001":  264,
		"277842155:mt5\x002002": 264, // 同 position 双腿同标
	}
	annotateWithIndex(bps, idx)
	if bps[0].PositionId != 264 {
		t.Errorf("bps[0] position_id=%d, want 264", bps[0].PositionId)
	}
	if bps[1].PositionId != 264 {
		t.Errorf("bps[1] position_id=%d, want 264（同 position 双腿同标）", bps[1].PositionId)
	}
}

// TestAnnotateWithIndex_Miss 未命中：broker ticket 不在索引中 → position_id=0（未关联）。
func TestAnnotateWithIndex_Miss(t *testing.T) {
	bps := []*v1.BrokerPosition{
		{VenueKey: "52993526:mt5", Ticket: "9999"}, // 孤儿订单
	}
	idx := map[string]int64{
		"52993526:mt5\x001001": 264,
	}
	annotateWithIndex(bps, idx)
	if bps[0].PositionId != 0 {
		t.Errorf("未命中 position_id=%d, want 0", bps[0].PositionId)
	}
}

// TestAnnotateWithIndex_SamePositionBothLegs 同 position 双腿同标（互腿一目了然）。
func TestAnnotateWithIndex_SamePositionBothLegs(t *testing.T) {
	bps := []*v1.BrokerPosition{
		{VenueKey: "52993526:mt5", Ticket: "T1"},
		{VenueKey: "277842155:mt5", Ticket: "T2"},
		{VenueKey: "REDACTED_MT_ACCT:mt5", Ticket: "T3"}, // 不同 position
	}
	idx := map[string]int64{
		"52993526:mt5\x00T1":  300,
		"277842155:mt5\x00T2": 300, // 同 position 300
		"REDACTED_MT_ACCT:mt5\x00T3": 301,
	}
	annotateWithIndex(bps, idx)
	if bps[0].PositionId != 300 || bps[1].PositionId != 300 {
		t.Errorf("同 position 双腿应同标 300, got %d/%d", bps[0].PositionId, bps[1].PositionId)
	}
	if bps[2].PositionId != 301 {
		t.Errorf("bps[2] position_id=%d, want 301", bps[2].PositionId)
	}
}

// TestAnnotateWithIndex_Empty 空输入不 panic。
func TestAnnotateWithIndex_Empty(t *testing.T) {
	annotateWithIndex(nil, map[string]int64{})     // 不 panic = pass
	annotateWithIndex([]*v1.BrokerPosition{}, nil) // 不 panic = pass
}

// --- F3 账号别名标注 ---

// TestAnnotateAliasWithMap_Hit 命中：venue 在 map 中 → account_alias 标注。
func TestAnnotateAliasWithMap_Hit(t *testing.T) {
	bps := []*v1.BrokerPosition{
		{Venue: "52993526", VenueKey: "52993526:mt5"},
		{Venue: "277842155", VenueKey: "277842155:mt5"},
	}
	m := map[string]string{
		"52993526":  "Exness 主号",
		"277842155": "PXBT 副号",
	}
	annotateAliasWithMap(bps, m)
	if bps[0].AccountAlias != "Exness 主号" {
		t.Errorf("bps[0] alias=%q, want \"Exness 主号\"", bps[0].AccountAlias)
	}
	if bps[1].AccountAlias != "PXBT 副号" {
		t.Errorf("bps[1] alias=%q, want \"PXBT 副号\"", bps[1].AccountAlias)
	}
}

// TestAnnotateAliasWithMap_Miss 未命中：venue 不在 map 中 → account_alias 留空（前端回落 venue）。
func TestAnnotateAliasWithMap_Miss(t *testing.T) {
	bps := []*v1.BrokerPosition{
		{Venue: "REDACTED_MT_ACCT", VenueKey: "REDACTED_MT_ACCT:mt5"}, // 未设别名
	}
	m := map[string]string{
		"52993526": "Exness 主号",
	}
	annotateAliasWithMap(bps, m)
	if bps[0].AccountAlias != "" {
		t.Errorf("未命中 alias=%q, want \"\"（前端回落 venue）", bps[0].AccountAlias)
	}
}

// TestAnnotateAliasWithMap_Empty 空输入不 panic。
func TestAnnotateAliasWithMap_Empty(t *testing.T) {
	annotateAliasWithMap(nil, map[string]string{})                // 不 panic = pass
	annotateAliasWithMap([]*v1.BrokerPosition{}, nil)             // 不 panic = pass
	annotateAliasWithMap([]*v1.BrokerPosition{{Venue: "v"}}, nil) // nil map 不 panic
}
