package dashboard

import (
	"strconv"
	"testing"
)

// D-127 P1：floorToStep 纯函数回归——缺陷①乘法伪影 + 缺陷③除法下溢。

func TestFloorToStep_MultiplicationArtifact(t *testing.T) {
	// 缺陷①：1.400639/0.01 → Floor(140.0639)=140 → 140*0.01=1.4000000000000001（17 位）
	// 修复后：round(140*0.01*100)/100 = round(140.000…03)/100 = 140/100 = 1.4
	got := floorToStep(1.400639, 0.01)
	if got != 1.4 {
		t.Fatalf("floorToStep(1.400639, 0.01)=%.17f，期望 1.4", got)
	}
	// 序列化必须 ≤3 位小数（非 17 位伪影）
	s := strconv.FormatFloat(got, 'f', -1, 64)
	if s != "1.4" {
		t.Errorf("FormatFloat(1.4,'f',-1)=%q，期望 \"1.4\"（无浮点伪影）", s)
	}
}

func TestFloorToStep_DivisionUnderflow(t *testing.T) {
	// 缺陷③：1137.4/0.01 = 113739.99999999999 → Floor=113739 → 1137.39（少 0.01）
	// 修复后：epsilon 1e-9 → 113740.000… → Floor=113740 → 1137.4
	got := floorToStep(1137.4, 0.01)
	if got != 1137.4 {
		t.Fatalf("floorToStep(1137.4, 0.01)=%.6f，期望 1137.4（非 1137.39 下溢）", got)
	}
}

func TestFloorToStep_DOTPrecision(t *testing.T) {
	// DOT step=0.1（D-127 P2 实测修正）：1144.033863 → Floor(11440.33…)=11440 → 1144
	got := floorToStep(1144.033863, 0.1)
	if got != 1144 {
		t.Fatalf("floorToStep(1144.033863, 0.1)=%.6f，期望 1144", got)
	}
}

func TestFloorToStep_StepOne(t *testing.T) {
	// TRX step=1：2964.7 → Floor(2964.7)=2964
	got := floorToStep(2964.7, 1)
	if got != 2964 {
		t.Fatalf("floorToStep(2964.7, 1)=%.6f，期望 2964", got)
	}
}

func TestFloorToStep_ZeroOrNegative(t *testing.T) {
	// step<=0 或 qty<=0 → 0（fail-closed）
	if v := floorToStep(100, 0); v != 0 {
		t.Errorf("floorToStep(100, 0)=%.6f，期望 0", v)
	}
	if v := floorToStep(100, -1); v != 0 {
		t.Errorf("floorToStep(100, -1)=%.6f，期望 0", v)
	}
	if v := floorToStep(0, 0.01); v != 0 {
		t.Errorf("floorToStep(0, 0.01)=%.6f，期望 0", v)
	}
	if v := floorToStep(-5, 0.01); v != 0 {
		t.Errorf("floorToStep(-5, 0.01)=%.6f，期望 0", v)
	}
}

func TestStepDecimals(t *testing.T) {
	cases := []struct {
		step float64
		want int
	}{
		{1, 0},
		{0.1, 1},
		{0.01, 2},
		{0.001, 3},
	}
	for _, c := range cases {
		if got := stepDecimals(c.step); got != c.want {
			t.Errorf("stepDecimals(%v)=%d，期望 %d", c.step, got, c.want)
		}
	}
}

// D-311 R4：floorToStepQty CEX 分支 venueKey 分派回归——
// gate:perp=1.0（整数张协议事实）；okx:perp=instId→lotSz（instruments 同源缓存）；
// binance=stepMeta 运行时表（D-466 撤硬编码 fallback）；其余→0 fail-closed。
func TestFloorToStepQty_CexVenueDispatch(t *testing.T) {
	s := &Service{
		okxLotSz: func(instID string) (float64, bool) {
			if instID == "CL-USDT-SWAP" {
				return 0.1, true
			}
			return 0, false
		},
	}
	// gate:perp → step=1.0 整数张（生产实证 CL_USDT qty=1010.407194 → 1010 张；
	// 旧码查 binance 表 step=0 → fail-closed qty=0 → buy 名义 $0 imbalance Abort）
	if got := s.floorToStepQty("gate:perp", "CL_USDT", 1010.407194); got != 1010 {
		t.Fatalf("gate:perp floorToStepQty=%v，期望 1010（step=1.0 整数张）", got)
	}
	// okx:perp → instId→lotSz=0.1 → 1010.407194 → 1010.4
	if got := s.floorToStepQty("okx:perp", "CL-USDT-SWAP", 1010.407194); got != 1010.4 {
		t.Fatalf("okx:perp floorToStepQty=%v，期望 1010.4（lotSz=0.1）", got)
	}
	// okx:perp 解析器取不到 → 0 fail-closed
	if got := s.floorToStepQty("okx:perp", "ZZZ-USDT-SWAP", 100); got != 0 {
		t.Fatalf("okx:perp 未知 instId floorToStepQty=%v，期望 0", got)
	}
	// binance:perp → stepMeta 运行时表（D-466 撤 fallback；注入 rt 表）
	sm := NewCexStepMeta("http://unused", 0)
	sm.mu.Lock()
	sm.rt = map[string]float64{"BTCUSDT": 0.001}
	sm.mu.Unlock()
	s.stepMeta = sm
	if got := s.floorToStepQty("binance:perp", "BTCUSDT", 1.2345); got != 1.234 {
		t.Fatalf("binance:perp floorToStepQty=%v，期望 1.234（runtime step=0.001）", got)
	}
	// stepMeta 未接线/未命中 → 0 fail-closed（硬编码表已撤）
	s3 := &Service{}
	if got := s3.floorToStepQty("binance:perp", "BTCUSDT", 1.5); got != 0 {
		t.Fatalf("binance:perp 未接线 floorToStepQty=%v，期望 0", got)
	}
	// 未知 venue → 0 fail-closed（不再蹭 binance fallback 表）
	if got := s.floorToStepQty("gate:spot", "BTC_USDT", 1.5); got != 0 {
		t.Fatalf("gate:spot floorToStepQty=%v，期望 0（非 binance 不查 binance 表）", got)
	}
	// okxLotSz 未接线 → 0 fail-closed
	s2 := &Service{}
	if got := s2.floorToStepQty("okx:perp", "CL-USDT-SWAP", 100); got != 0 {
		t.Fatalf("okx:perp 未接线 floorToStepQty=%v，期望 0", got)
	}
}
