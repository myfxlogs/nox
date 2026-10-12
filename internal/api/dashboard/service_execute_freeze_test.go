package dashboard

import (
	"strings"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/trading/exec"
)

// makeFreezeBus 构造一个 QuoteBus：buy 腿价格活跃（已变化），sell 腿可选活跃/冻结。
// activeSell=true → sell 腿也变化；false → sell 腿仅首见（冻结）。
func makeFreezeBus(activeSell bool) *bus.QuoteBus {
	b := bus.New()
	now := time.Now()
	// buy 腿：首见 + 价格变化 → 活跃
	b.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "g", VenueType: "mt5", Bid: 2000, Ask: 2000.2, Time: now, VenueSymbol: "GOLD365"})
	b.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "g", VenueType: "mt5", Bid: 2001, Ask: 2001.2, Time: now.Add(time.Second), VenueSymbol: "GOLD365"})
	// sell 腿：首见
	b.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "p", VenueType: "mt5", Bid: 2000.1, Ask: 2000.3, Time: now, VenueSymbol: "XAUUSD"})
	if activeSell {
		// sell 腿价格变化 → 活跃
		b.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "p", VenueType: "mt5", Bid: 2000.5, Ask: 2000.7, Time: now.Add(time.Second), VenueSymbol: "XAUUSD"})
	}
	return b
}

func freezeLegs() []exec.LegSpec {
	return []exec.LegSpec{
		{VenueKey: "g:mt5", VenueSymbol: "GOLD365", Side: "BUY", Qty: 0.1, PriceExpected: 2001.2},
		{VenueKey: "p:mt5", VenueSymbol: "XAUUSD", Side: "SELL", Qty: 0.1, PriceExpected: 2000.3},
	}
}

// TestCheckFreezeGuard_FrozenLegAborts D-155：任一腿冻结 → 整组 abort（LEGGED 零容忍）。
// buy 腿活跃、sell 腿冻结（仅首见）→ 拒绝整组，错误含冻结腿 venueKey。
func TestCheckFreezeGuard_FrozenLegAborts(t *testing.T) {
	b := makeFreezeBus(false) // sell 腿冻结
	s := &Service{qbus: b, freezeGuard: 10 * time.Minute}

	err := s.checkFreezeGuard("XAUUSD", "XAUUSD", freezeLegs())
	if err == nil {
		t.Fatal("冻结腿应拒绝整组，got nil")
	}
	if !strings.Contains(err.Error(), "p:mt5") {
		t.Errorf("错误应含冻结腿 venueKey p:mt5, got %v", err)
	}
	if !strings.Contains(err.Error(), "冻结") {
		t.Errorf("错误应含冻结, got %v", err)
	}
}

// TestCheckFreezeGuard_BothActivePass D-155：双腿活跃（窗口内价格变化）→ 放行。
func TestCheckFreezeGuard_BothActivePass(t *testing.T) {
	b := makeFreezeBus(true) // 双腿活跃
	s := &Service{qbus: b, freezeGuard: 10 * time.Minute}

	if err := s.checkFreezeGuard("XAUUSD", "XAUUSD", freezeLegs()); err != nil {
		t.Fatalf("双腿活跃应放行, got %v", err)
	}
}

// TestCheckFreezeGuard_DisabledAllPass D-155：freezeGuard≤0 禁用 → 全放行（回归）。
// 即使双腿冻结也放行（开关关闭=回退旧行为）。
func TestCheckFreezeGuard_DisabledAllPass(t *testing.T) {
	b := makeFreezeBus(false) // 双腿冻结
	s := &Service{qbus: b, freezeGuard: 0}

	if err := s.checkFreezeGuard("XAUUSD", "XAUUSD", freezeLegs()); err != nil {
		t.Fatalf("禁用开关应全放行, got %v", err)
	}
	// 负值同样禁用
	s.freezeGuard = -1
	if err := s.checkFreezeGuard("XAUUSD", "XAUUSD", freezeLegs()); err != nil {
		t.Fatalf("负值禁用应全放行, got %v", err)
	}
}
