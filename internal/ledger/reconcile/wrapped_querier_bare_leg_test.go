// d202_test.go —— D-202 锚：CEX 聚合对拍径全瘫三层修——
// ①wrapped querier（类型断言穿透失效）仍中分类；②裸 venue 腿（"okx" R1）归集；
// ③1000 张对拍静默；真外部单仍 warn。
package reconcile

import (
	"context"
	"testing"
	"time"

	"strings"

	"nox/internal/data/venue"
	"nox/internal/store/pgstore"
)

// wrapQ 装饰层 querier：非 *CEXPositionQuerier 具体型（D-202①根因形——
// 类型断言必假=分类全瘫），VenueKey 委托内层。
type wrapQ struct{ inner venue.Querier }

func (w wrapQ) QueryOpenedOrders(ctx context.Context) ([]venue.Order, error) {
	return w.inner.QueryOpenedOrders(ctx)
}
func (w wrapQ) VenueKey() string { return w.inner.VenueKey() }

// ①+②+③：wrapped querier + 裸 venue 腿 → 归集对拍静默（venue=local=1000 张）。
func TestD202_WrappedQuerierBareLeg(t *testing.T) {
	srv := okxPosStub(t, "XAU-USDT-SWAP", "short", "1000")
	defer srv.Close()
	fs := newFakeStore()
	fs.positions[510] = mkPosition(510, "OPEN", "CROSS_VENUE", time.Now().Add(-time.Hour))
	// #510 真实形：leg.Venue="okx" 裸端点（R1），qty=1000 张（D-385 钦定 venue 张数）。
	fs.legs[510] = []*pgstore.PositionLeg{mkOpenLeg(510, 0, "okx", "ordY", "SELL", "XAU-USDT-SWAP", 1000)}
	r := New(fs, wrapQ{okxQuerier(srv)}) // 装饰层包覆——旧断言径必瘫
	r.runTimeout = 5 * time.Second

	mm, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if mm != 0 {
		t.Fatalf("wrapped querier 应中聚合分类且静默: mm=%d alerts=%v", mm, fs.alerts)
	}
	for _, a := range fs.alerts {
		if strings.Contains(a.msg, "查证器未就绪") || strings.Contains(a.msg, "系统外单") {
			t.Fatalf("不应告警: %v", a)
		}
	}
}

// 真外部单仍 warn：venue-only 持仓（本地零腿）→ 系统外单 warn 不静默。
func TestD202_ForeignStillWarns(t *testing.T) {
	srv := okxPosStub(t, "XAU-USDT-SWAP", "long", "500")
	defer srv.Close()
	fs := newFakeStore()
	r := New(fs, wrapQ{okxQuerier(srv)})
	r.runTimeout = 5 * time.Second
	mm, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if mm != 0 {
		t.Fatalf("系统外单不计 mismatch: %d", mm)
	}
	if !hasAlert(fs, "warn", "系统外单") {
		t.Fatalf("真外部单 warn 缺: %v", fs.alerts)
	}
}
