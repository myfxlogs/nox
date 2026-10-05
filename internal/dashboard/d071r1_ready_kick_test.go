// d071r1_ready_kick_test.go —— D-071 R1 锚：adapter ready 点补 venueKey 定向 kick。
// 生产残余竞态：margin 首采 kick 可早于 adapter ready（15032313 实拍早 11.4s=
// 8514 符号 backfill）→ 定向直读打空且此后零事件 → venue 永缺。ready=querier
// 可读权威时点，补 kick 一次（幂等）；未接线 nil 安全。
package dashboard

import "testing"

func TestD071R1_ReadyKickCarriesVenue(t *testing.T) {
	m := &BrokerManager{}
	var got []string
	m.SetOrderProfitKick(func(vk string) { got = append(got, vk) })
	m.kickAccountReady("vA:mt5")
	if len(got) != 1 || got[0] != "vA:mt5" {
		t.Fatalf("ready kick want [vA:mt5], got %v", got)
	}
	// 幂等：重复 ready kick 只是再发一次定向无防抖直读（重复 poll 无害）。
	m.kickAccountReady("vA:mt5")
	if len(got) != 2 || got[1] != "vA:mt5" {
		t.Fatalf("second kick want appended, got %v", got)
	}
	// nil 安全：未接线零 panic。
	(&BrokerManager{}).kickAccountReady("vA:mt5")
}
