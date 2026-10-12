package exec

// d570c_gate_test.go —— D-570 包C 验收锚 A12/A17（exec 侧：包级缓存+TTL+拒单驱动失效）。
import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A17+A12：GateSpecInvalidate 删双表（Load 双 miss 断言）；at>24h 条目视 miss 重取（计数+1）；
// INVALID_PRECISION 拒单→失效（双表删）。
func TestD570C_A17_InvalidateBothCaches(t *testing.T) {
	gateSpecCache.Store("A17_USDT", gateContractSpec{Quanto: 1, at: time.Now()})
	gateQuantoCache.Store("A17_USDT", 1.0)
	GateSpecInvalidate("A17_USDT")
	if _, ok := gateSpecCache.Load("A17_USDT"); ok {
		t.Fatalf("GateSpecInvalidate 后 spec 仍命中（漏删→A17 红）")
	}
	if _, ok := gateQuantoCache.Load("A17_USDT"); ok {
		t.Fatalf("GateSpecInvalidate 后 quanto 仍命中（漏删→A17 红）")
	}
}

func TestD570C_A12_TTLAndRejectInvalidate(t *testing.T) {
	GateSpecInvalidate("TTL_USDT") // 包级表跨 -count=N 迭代共享——本测试键自清
	var fetchN atomic.Int32
	var orderN atomic.Int32
	h := map[string]func(http.ResponseWriter, *http.Request){
		"/api/v4/futures/usdt/contracts/TTL_USDT": func(w http.ResponseWriter, r *http.Request) {
			fetchN.Add(1)
			_, _ = io.WriteString(w, d570Spec(`"10"`, `"trading"`))
		},
		"POST /api/v4/futures/usdt/orders": func(w http.ResponseWriter, r *http.Request) {
			orderN.Add(1)
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"label":"INVALID_PRECISION","message":"bad size"}`)
		},
	}
	f := newD570Fixture(t, h)
	ctx := context.Background()
	if _, err := f.e.gateSpecFetch(ctx, "TTL_USDT"); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	// 新鲜缓存命中——不再取。
	if _, err := f.e.gateSpecFetch(ctx, "TTL_USDT"); err != nil || fetchN.Load() != 1 {
		t.Fatalf("新鲜条目应命中零重取: err=%v n=%d", err, fetchN.Load())
	}
	// 人工造 >24h 陈条目 → 视 miss 重取（计数+1；不判龄→红）。
	if v, ok := gateSpecCache.Load("TTL_USDT"); ok {
		s := v.(gateContractSpec)
		s.at = time.Now().Add(-25 * time.Hour)
		gateSpecCache.Store("TTL_USDT", s)
	}
	if _, err := f.e.gateSpecFetch(ctx, "TTL_USDT"); err != nil || fetchN.Load() != 2 {
		t.Fatalf("at>24h 应 miss 重取: err=%v n=%d", err, fetchN.Load())
	}
	// 拒单驱动：INVALID_PRECISION → 双表失效（删失效代码→缓存残留=红）。
	if _, err := f.e.placeGate(ctx, Order{Symbol: "TTL_USDT", Side: "BUY", Qty: 1}); err == nil {
		t.Fatalf("INVALID_PRECISION 应上抛")
	}
	if _, ok := gateSpecCache.Load("TTL_USDT"); ok {
		t.Fatalf("INVALID_PRECISION 拒单后 spec 仍缓存（拒单驱动失效漏→红）")
	}
	if _, ok := gateQuantoCache.Load("TTL_USDT"); ok {
		t.Fatalf("INVALID_PRECISION 拒单后 quanto 仍缓存")
	}
	// 失效后下次 fetch 重取（链回弹）。
	if _, err := f.e.gateSpecFetch(ctx, "TTL_USDT"); err != nil || fetchN.Load() != 3 {
		t.Fatalf("失效后应重取: err=%v n=%d", err, fetchN.Load())
	}
}

// AMOUNT_TOO_LITTLE / AMOUNT_TOO_MUCH 同标签族一钉（防只实现单标签）。
func TestD570C_A12_RejectLabelsFamily(t *testing.T) {
	for _, lab := range []string{"AMOUNT_TOO_LITTLE", "AMOUNT_TOO_MUCH"} {
		contract := strings.ReplaceAll(lab, "_", "") + "_USDT"
		GateSpecInvalidate(contract)
		h := map[string]func(http.ResponseWriter, *http.Request){
			"/api/v4/futures/usdt/contracts/" + contract: func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, d570Spec(`"10"`, `"trading"`))
			},
			"POST /api/v4/futures/usdt/orders": func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"label":"`+lab+`","message":"x"}`)
			},
		}
		f := newD570Fixture(t, h)
		ctx := context.Background()
		if _, err := f.e.gateSpecFetch(ctx, contract); err != nil {
			t.Fatalf("%s fetch: %v", lab, err)
		}
		if _, err := f.e.placeGate(ctx, Order{Symbol: contract, Side: "BUY", Qty: 1}); err == nil {
			t.Fatalf("%s 应上抛", lab)
		}
		if _, ok := gateSpecCache.Load(contract); ok {
			t.Fatalf("%s 拒单后 spec 应失效", lab)
		}
		GateSpecInvalidate(contract)
	}
}
