package dashboard

import (
	"math"
	"testing"
	"time"

	"arb/internal/data/bus"
	"arb/internal/platform/usdnotional"
)

// TestSwapArbRatio_Default ratio=0.5 生效（保留抗单余地）。
func TestSwapArbRatio_Default(t *testing.T) {
	if got := swapArbRatio(0.5); got != 0.5 {
		t.Errorf("swapArbRatio(0.5)=%v, want 0.5", got)
	}
}

// TestSwapArbRatio_Equity10K_Notional5K ratio=0.5 → equity 10K 折半 5K。
func TestSwapArbRatio_Equity10K_Notional5K(t *testing.T) {
	equity := 10000.0
	ratio := swapArbRatio(0.5)
	notional := equity * ratio
	if math.Abs(notional-5000) > 0.01 {
		t.Errorf("equity 10K × ratio 0.5 = %v, want 5000", notional)
	}
}

// TestSwapArbRatio_One_EquivalentOld ratio=1 等价旧行为（100% 资金使用率）。
func TestSwapArbRatio_One_EquivalentOld(t *testing.T) {
	if got := swapArbRatio(1); got != 1 {
		t.Errorf("swapArbRatio(1)=%v, want 1（等价旧行为）", got)
	}
}

// TestSwapArbRatio_Zero_UnwiredEquivalentOld ratio=0（未接线测试）等价 ratio=1 旧行为。
func TestSwapArbRatio_Zero_UnwiredEquivalentOld(t *testing.T) {
	if got := swapArbRatio(0); got != 1 {
		t.Errorf("swapArbRatio(0)=%v, want 1（未接线等价旧行为）", got)
	}
}

// TestSwapArbRatio_OverOne_Clamped ratio>1 钳到 1（防误配放大）。
func TestSwapArbRatio_OverOne_Clamped(t *testing.T) {
	if got := swapArbRatio(1.5); got != 1 {
		t.Errorf("swapArbRatio(1.5)=%v, want 1（>1 钳到 1 防误配放大）", got)
	}
}

// TestSwapArbRatio_Negative_Clamped 负值钳到 1。
func TestSwapArbRatio_Negative_Clamped(t *testing.T) {
	if got := swapArbRatio(-0.1); got != 1 {
		t.Errorf("swapArbRatio(-0.1)=%v, want 1（负值钳到 1）", got)
	}
}

// --- A 修复：usdPerLot 币种换算单测 ---

// TestUSDPerLot_USDPair USD 计价（BTCUSDT）→ CS×price 直接美元名义。
func TestUSDPerLot_USDPair(t *testing.T) {
	qbus := bus.New()
	got, ok := usdnotional.PerLot("BTCUSDT", 50000, 1, qbus)
	if !ok {
		t.Fatal("USD pair should be ok")
	}
	if math.Abs(got-50000) > 0.01 {
		t.Errorf("USD pair CS×price want 50000, got %v", got)
	}
}

// TestUSDPerLot_CommodityBRENT 商品 CFD（BRENT）无 USD 后缀但 USD 计价 → CS×price 直接美元名义。
// D-166 修复：BRENT/WTI/SILVER 等商品 canonical 不以 USD/USDT/JPY 结尾，原 default 分支误拒。
func TestUSDPerLot_CommodityBRENT(t *testing.T) {
	qbus := bus.New()
	got, ok := usdnotional.PerLot("BRENT", 92.5, 1000, qbus)
	if !ok {
		t.Fatal("BRENT (USD-quoted commodity) should be ok")
	}
	if math.Abs(got-92500) > 0.01 {
		t.Errorf("BRENT CS×price want 92500, got %v", got)
	}
}

// TestUSDPerLot_CommodityWTI WTI 同 BRENT，USD 计价商品白名单。
func TestUSDPerLot_CommodityWTI(t *testing.T) {
	qbus := bus.New()
	got, ok := usdnotional.PerLot("WTI", 85.0, 1000, qbus)
	if !ok {
		t.Fatal("WTI (USD-quoted commodity) should be ok")
	}
	if math.Abs(got-85000) > 0.01 {
		t.Errorf("WTI CS×price want 85000, got %v", got)
	}
}

// TestUSDPerLot_IndexNAS100 指数 CFD（NAS100）USD 计价白名单。
func TestUSDPerLot_IndexNAS100(t *testing.T) {
	qbus := bus.New()
	got, ok := usdnotional.PerLot("NAS100", 20000, 1, qbus)
	if !ok {
		t.Fatal("NAS100 (USD-quoted index) should be ok")
	}
	if math.Abs(got-20000) > 0.01 {
		t.Errorf("NAS100 CS×price want 20000, got %v", got)
	}
}

// TestUSDPerLot_JPYPair JPY 计价（AUDJPY）→ CS×price÷USDJPY 换算美元。
// AUDJPY price=113.5, CS=100000 → JPY 名义 11,350,000 ÷ USDJPY 150 ≈ $75,667（与 #264 历史吻合量级）。
func TestUSDPerLot_JPYPair(t *testing.T) {
	qbus := bus.New()
	qbus.Publish(bus.Quote{Symbol: "USDJPY", Venue: "fx", Bid: 149.9, Ask: 150.1, Time: time.Now()})
	got, ok := usdnotional.PerLot("AUDJPY", 113.5, 100000, qbus)
	if !ok {
		t.Fatal("JPY pair with USDJPY quote should be ok")
	}
	// 100000 × 113.5 ÷ 150 = 75666.67
	if math.Abs(got-75666.67) > 1 {
		t.Errorf("JPY pair CS×price÷USDJPY want ~75666.67, got %v", got)
	}
}

// TestUSDPerLot_JPYPair_FailClosed USDJPY 报价缺失 → ok=false fail-closed（禁止硬编码汇率）。
func TestUSDPerLot_JPYPair_FailClosed(t *testing.T) {
	qbus := bus.New() // 无 USDJPY 报价
	_, ok := usdnotional.PerLot("AUDJPY", 113.5, 100000, qbus)
	if ok {
		t.Error("JPY pair without USDJPY quote should fail-closed (ok=false)")
	}
}

// TestUSDPerLot_UnsupportedCurrency 未支持计价币（GBP/EUR 等）→ ok=false（勿猜）。
func TestUSDPerLot_UnsupportedCurrency(t *testing.T) {
	qbus := bus.New()
	_, ok := usdnotional.PerLot("EURGBP", 0.85, 100000, qbus)
	if ok {
		t.Error("Unsupported currency should fail-closed (ok=false)")
	}
}

// TestUSDPerLot_InvalidInput price/CS ≤ 0 → ok=false。
func TestUSDPerLot_InvalidInput(t *testing.T) {
	qbus := bus.New()
	if _, ok := usdnotional.PerLot("BTCUSDT", 0, 1, qbus); ok {
		t.Error("price=0 should fail")
	}
	if _, ok := usdnotional.PerLot("BTCUSDT", 50000, 0, qbus); ok {
		t.Error("CS=0 should fail")
	}
}
