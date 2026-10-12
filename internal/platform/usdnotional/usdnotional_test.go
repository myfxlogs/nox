package usdnotional

import (
	"testing"
	"time"

	"nox/internal/data/bus"
)

func TestPerLot_USDSuffix(t *testing.T) {
	// EURUSD: CS=100000, price=1.0850 → 108500
	v, ok := PerLot("EURUSD", 1.0850, 100000, nil)
	if !ok {
		t.Fatal("expected ok")
	}
	if v != 108500 {
		t.Errorf("PerLot EURUSD = %v, want 108500", v)
	}
}

func TestPerLot_USDTSuffix(t *testing.T) {
	// BTCUSDT: CS=1, price=50000 → 50000
	v, ok := PerLot("BTCUSDT", 50000, 1, nil)
	if !ok {
		t.Fatal("expected ok")
	}
	if v != 50000 {
		t.Errorf("PerLot BTCUSDT = %v, want 50000", v)
	}
}

func TestPerLot_CommodityWhitelist(t *testing.T) {
	// BRENT: CS=1000, price=70 → 70000（无 USD 后缀但白名单命中）
	v, ok := PerLot("BRENT", 70, 1000, nil)
	if !ok {
		t.Fatal("expected ok (commodity whitelist)")
	}
	if v != 70000 {
		t.Errorf("PerLot BRENT = %v, want 70000", v)
	}
}

func TestPerLot_JPYSuffix(t *testing.T) {
	// AUDJPY: CS=100000, price=97, USDJPY=150 → 100000*97/150 = 64666.67
	qb := bus.New()
	qb.Publish(bus.Quote{Symbol: "USDJPY", Bid: 149.9, Ask: 150.1, Time: time.Now()})
	v, ok := PerLot("AUDJPY", 97, 100000, qb)
	if !ok {
		t.Fatal("expected ok")
	}
	want := 100000.0 * 97.0 / 150.0
	if v < want-1 || v > want+1 {
		t.Errorf("PerLot AUDJPY = %v, want ~%v", v, want)
	}
}

func TestPerLot_JPYFxMissing(t *testing.T) {
	// JPY 腿但无 USDJPY 报价 → false
	qb := bus.New()
	_, ok := PerLot("AUDJPY", 97, 100000, qb)
	if ok {
		t.Error("expected false (no USDJPY quote)")
	}
}

func TestPerLot_CSZero(t *testing.T) {
	_, ok := PerLot("EURUSD", 1.0850, 0, nil)
	if ok {
		t.Error("expected false (CS=0)")
	}
}

func TestPerLot_PriceZero(t *testing.T) {
	_, ok := PerLot("EURUSD", 0, 100000, nil)
	if ok {
		t.Error("expected false (price=0)")
	}
}

func TestPerLot_UnknownCurrency(t *testing.T) {
	// EURGBP: 非 USD/USDT/JPY/白名单 → false
	_, ok := PerLot("EURGBP", 0.85, 100000, nil)
	if ok {
		t.Error("expected false (EURGBP unsupported)")
	}
}

func TestPerLot_QbusNil_JPY(t *testing.T) {
	// JPY 腿 + qbus nil → false
	_, ok := PerLot("AUDJPY", 97, 100000, nil)
	if ok {
		t.Error("expected false (qbus nil on JPY leg)")
	}
}
