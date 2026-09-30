package dashboard

import (
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/venue"
)

// TestBrokerPositionToProto_SLPassthrough D-155：MT 持仓 broker 端 SL 值透传。
// venue.Order.StopLoss 非零 → BrokerPosition.sl 透传（D-146 S1 灾难 SL 生产验证基础设施）。
func TestBrokerPositionToProto_SLPassthrough(t *testing.T) {
	o := venue.Order{
		Venue: "277842155", VenueKey: "277842155:mt5", Ticket: "316806363",
		Symbol: "XAUUSDm", Side: "BUY", Volume: 0.1, OpenPrice: 2000.0,
		Profit: 12.34, Comment: "arb", Magic: 777,
		StopLoss: 1960.0, // D-146 S1 灾难 SL：entry × (1 − 2%) = 1960
	}
	pb := brokerPositionToProto(o, "XAUUSD", bus.Quote{}, false, true, time.Minute, time.Now(), 0, nil, 0)
	if pb.Sl != 1960.0 {
		t.Errorf("SL 透传 want 1960.0, got %v", pb.Sl)
	}
}

// TestBrokerPositionToProto_SLZeroEmpty D-155：无 SL（StopLoss=0）→ BrokerPosition.sl=0（诚实空态）。
// CEX 持仓无 SL 概念 / MT 未设 SL → 0。
func TestBrokerPositionToProto_SLZeroEmpty(t *testing.T) {
	o := venue.Order{
		Venue: "binance", VenueKey: "binance:perp", Ticket: "BTCUSDT",
		Symbol: "BTCUSDT", Side: "BUY", Volume: 0.001, OpenPrice: 50000,
		Comment: "arb",
		// CEX 无 SL 概念 → StopLoss 零值
	}
	pb := brokerPositionToProto(o, "BTCUSDT", bus.Quote{}, false, true, time.Minute, time.Now(), 0, nil, 0)
	if pb.Sl != 0 {
		t.Errorf("无 SL 应透传 0, got %v", pb.Sl)
	}
}
