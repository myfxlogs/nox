// d555_approve_test.go —— D-555 S4：双CEX（long_spot_short_perp）毕业授权锚。
// 覆盖：腿键行数据合成（gate:spot×binance:perp）/combo 落库/simulated=false/
// canonical 同值/MT 径零触达（platform 查库、symbol map、resolver 全旁路）/
// 反例：腿型不符、canonical 空/不符、venue 缺、未知结构、reloader 缺接线。
package dashboard

import (
	"context"
	"strings"
	"testing"
	"time"

	"arb/internal/exec"
	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/store/pgstore"
)

// d555CexFixture 双CEX 毕业行夹具：A=gate:spot（买腿）× B=binance:perp（卖腿），
// mt_venue=nil——approve 若误入 cfd 分支会「mt_venue 缺失」拒，绿即证零 MT 径。
func d555CexFixture() (*fakeGradDB, *Service, *int) {
	fee, entryB, lastB := 0.5, 12.0, 10.0 // short_perp dir=−1：收敛 +0.2
	f := &fakeGradDB{
		grad: &pgstore.CarryGraduation{ID: 5, PaperID: 42, VenueA: "gate", VenueB: "binance",
			VenueTypeA: "spot", VenueTypeB: "perp",
			Symbol: "BTC", Structure: "long_spot_short_perp", Status: "pending"},
		paper: &pgstore.PaperSpotCarry{ID: 42, VenueA: "gate", VenueB: "binance",
			VenueTypeA: "spot", VenueTypeB: "perp",
			Symbol: "BTC", Structure: "long_spot_short_perp", Side: "short_perp",
			Status: "active", AccruedUSD: 5.0, EntryTs: time.Now().Add(-6 * 24 * time.Hour),
			QtyUSD:             1000,
			EstRoundTripFeeUSD: &fee, EntryBasisBps: &entryB, LastBasisBps: &lastB},
		accts: []pgstore.BrokerAccount{{Venue: "1279967", Platform: 0}}, // MT4 退役平台——误触 MT 径必拒
	}
	s := &Service{
		gradStore: f, xassetReloader: &fakeReloader{},
		allowVenuesSource: func(context.Context) (map[string]bool, error) {
			return map[string]bool{"gate:spot": true, "binance:perp": true}, nil
		},
		xassetEnv: "real",
		pipeline:  exec.NewPipeline(exec.NewRegistry(), nil, nil, exec.RiskLimits{}, "real"),
	}
	autoMapCalls := new(int)
	s.autoMapFn = func(context.Context, string) (string, error) {
		*autoMapCalls++
		return "", nil
	}
	return f, s, autoMapCalls
}

func TestD555S4_ApproveCexCex(t *testing.T) {
	f, s, autoMapCalls := d555CexFixture()
	resp, err := s.ApproveCarryGraduation(context.Background(), approveReq())
	if err != nil || !resp.Msg.Ok {
		t.Fatalf("ok=%v err=%v errstr=%s", resp.Msg.Ok, err, resp.Msg.Error)
	}
	if f.combo == nil {
		t.Fatal("combo 未写入")
	}
	c := f.combo
	if c.CanonicalBuy != "BTC" || c.CanonicalSell != "BTC" {
		t.Errorf("canonical 应双腿同值 BTC，got %q/%q", c.CanonicalBuy, c.CanonicalSell)
	}
	if c.VenueBuy != "gate:spot" || c.VenueSell != "binance:perp" {
		t.Errorf("venue 腿键应为行数据合成 gate:spot×binance:perp，got %q/%q", c.VenueBuy, c.VenueSell)
	}
	if c.Simulated {
		t.Error("双CEX 毕业授权 combo 必须 simulated=false")
	}
	if *autoMapCalls != 0 {
		t.Errorf("automapper（MT symbol map 径）不应触达，calls=%d", *autoMapCalls)
	}
	if resp.Msg.ComboId == nil {
		t.Error("应答缺 combo_id")
	}
}

func TestD555S4_ApproveCexCex_Negative(t *testing.T) {
	approve := func(mutate func(f *fakeGradDB)) (*v1.ApproveCarryGraduationResponse, *fakeGradDB) {
		f, s, _ := d555CexFixture()
		mutate(f)
		resp, _ := s.ApproveCarryGraduation(context.Background(), approveReq())
		return resp.Msg, f
	}
	t.Run("腿型非 spot×perp 拒", func(t *testing.T) {
		m, f := approve(func(f *fakeGradDB) { f.grad.VenueTypeA = "perp" })
		if m.Ok || !strings.Contains(m.Error, "腿型不符") {
			t.Fatalf("ok=%v err=%q", m.Ok, m.Error)
		}
		if f.combo != nil {
			t.Error("拒后不得写白名单")
		}
	})
	t.Run("symbol 空拒", func(t *testing.T) {
		m, _ := approve(func(f *fakeGradDB) { f.grad.Symbol = "" })
		if m.Ok || !strings.Contains(m.Error, "canonical 构型不符") {
			t.Fatalf("ok=%v err=%q", m.Ok, m.Error)
		}
	})
	t.Run("grad×paper canonical 不符拒", func(t *testing.T) {
		m, _ := approve(func(f *fakeGradDB) { f.paper.Symbol = "ETH" })
		if m.Ok || !strings.Contains(m.Error, "canonical 构型不符") {
			t.Fatalf("ok=%v err=%q", m.Ok, m.Error)
		}
	})
	t.Run("venue 缺拒", func(t *testing.T) {
		m, _ := approve(func(f *fakeGradDB) { f.grad.VenueB = "" })
		if m.Ok || !strings.Contains(m.Error, "腿型不符") {
			t.Fatalf("ok=%v err=%q", m.Ok, m.Error)
		}
	})
	t.Run("未知结构 fail-closed", func(t *testing.T) {
		m, f := approve(func(f *fakeGradDB) {
			f.grad.Structure, f.paper.Structure = "perp_funding", "perp_funding"
		})
		if m.Ok || f.combo != nil {
			t.Fatalf("ok=%v combo=%v err=%q", m.Ok, f.combo, m.Error)
		}
	})
	t.Run("reloader 缺接线 fail-closed", func(t *testing.T) {
		f, s, _ := d555CexFixture()
		s.xassetReloader = nil
		resp, _ := s.ApproveCarryGraduation(context.Background(), approveReq())
		if resp.Msg.Ok || !strings.Contains(resp.Msg.Error, "重载器") || f.combo != nil {
			t.Fatalf("ok=%v err=%q combo=%v", resp.Msg.Ok, resp.Msg.Error, f.combo)
		}
	})
}

// TestD555S4_ApproveCfdRegression cfd 径回归钉：S4 改动不触 cfd 分支语义——
// mt_venue platform 闸、方向映射、白名单写入全绿（与 D403 既有锚互补）。
func TestD555S4_ApproveCfdRegression(t *testing.T) {
	f, s := gradFixture("long_cfd_short_perp")
	resp, err := s.ApproveCarryGraduation(context.Background(), approveReq())
	if err != nil || !resp.Msg.Ok {
		t.Fatalf("ok=%v err=%v errstr=%s", resp.Msg.Ok, err, resp.Msg.Error)
	}
	if f.combo == nil || f.combo.CanonicalSell != "BTCUSDT" || f.combo.VenueSell != "gate:perp" {
		t.Fatalf("cfd 方向/腿键回归失败: %+v", f.combo)
	}
}
