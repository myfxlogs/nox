package dciexec

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nox/internal/platform/errclass"
	"nox/internal/platform/testutil/storetest"
	"nox/internal/store/pgstore"
)

// TestD255_Signature 败文→签名归一锚：code 取码；非 JSON 归一 msg: 前缀。
func TestD255_Signature(t *testing.T) {
	cases := map[string]string{
		`subscribe HTTP 502: {"code":-1102,"msg":"Mandatory parameter 'projectId' was not sent"}`: "code:-1102",
		`subscribe HTTP 502: {"code":-9000,"msg":"APY updated refresh"}`:                          "code:-9000",
		`subscribe HTTP 502: gateway timeout`:                                                     "msg:subscribe http #: gateway timeout",
	}
	for in, want := range cases {
		if got := errclass.VenueRejectSignature(in); got != want {
			t.Errorf("%q → %q want %q", in, got, want)
		}
	}
}

// TestD255_PitSuppress 钦定验收锚：同一败因二次触发零 venue 打面——
// 首撞 -9000 落坑 suppress；次拍 tryLeg 坑表拦单零 POST，order=rejected+critical 留痕。
func TestD255_PitSuppress(t *testing.T) {
	fs := newFake()
	postCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if dciListOK(w, r) {
			return
		}
		if r.Method == http.MethodPost {
			postCalls++
			w.WriteHeader(502)
			_, _ = w.Write([]byte(`{"code":-9000,"msg":"APY updated refresh"}`))
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	e := execR3(fs, srv.URL, srv.Client(), map[string]float64{"BTC": 0.2, "USDT": 0})

	if _, err := e.GoWheel(context.Background()); err != nil {
		t.Fatal(err)
	}
	if postCalls != 2 {
		t.Fatalf("首拍 post 次=%d（-9000 msg 含 refresh→既有重提径一次，共 2 次）", postCalls)
	}
	var pit *pgstore.VenueReject
	for i := range fs.pits {
		if fs.pits[i].ErrorSignature == "code:-9000" {
			pit = &fs.pits[i]
		}
	}
	if pit == nil || pit.Action != "suppress" {
		t.Fatalf("首撞应落 suppress 坑: %+v", fs.pits)
	}

	// 二拍：坑窗内——零 POST 增，订单行 rejected，critical 抑单告警留痕
	if _, err := e.GoWheel(context.Background()); err != nil {
		t.Fatal(err)
	}
	if postCalls != 2 {
		t.Fatalf("二次触发应零 venue 打面, postCalls=%d", postCalls)
	}
	var rejected int
	for _, o := range fs.orders {
		if o.Status == "rejected" && strings.Contains(o.Response, "坑表抑单") {
			rejected++
		}
	}
	if rejected == 0 {
		t.Fatalf("抑单订单行缺: %+v", fs.orders)
	}
	var crit string
	for _, a := range fs.alerts {
		if strings.Contains(a, "critical") && strings.Contains(a, "坑表抑单") {
			crit = a
		}
	}
	if crit == "" {
		t.Fatalf("抑单 critical 缺: %v", fs.alerts)
	}
}

// TestD255_PitFix 坑表 fix 映射锚：deposit_decimals=0 行→depositAmount floor 整数重发。
func TestD255_PitFix(t *testing.T) {
	if got := applyRejectFix(0.123456, []byte(`{"deposit_decimals":0}`)); got != 0 {
		t.Fatalf("fix 应 floor 整数: %v", got)
	}
	if got := applyRejectFix(0.123456, []byte(`{}`)); got != 0.123456 {
		t.Fatalf("无映射不改: %v", got)
	}
}

// TestD255_PgRoundtrip 真实测试库坑表读写：learn upsert hits 累加+expires 窗。
func TestD255_PgRoundtrip(t *testing.T) {
	st := storetest.SharedStore(t, "NOX_TEST_PG_DSN")
	ctx := context.Background()
	defer func() {
		_, _ = st.Pool().Exec(ctx, `DELETE FROM fin_venue_reject WHERE venue='t' AND op='dci_subscribe'`)
	}()
	if err := st.LearnVenueReject(ctx, "t", "dci_subscribe", "code:-1102", 24*time.Hour); err != nil {
		t.Fatalf("learn: %v", err)
	}
	if err := st.LearnVenueReject(ctx, "t", "dci_subscribe", "code:-1102", 24*time.Hour); err != nil {
		t.Fatalf("learn2: %v", err)
	}
	pits, err := st.ListVenueRejects(ctx, "t", "dci_subscribe")
	if err != nil || len(pits) != 1 {
		t.Fatalf("list: %v n=%d", err, len(pits))
	}
	if pits[0].Hits != 2 || pits[0].ExpiresAt == nil || !pits[0].Active(time.Now()) {
		t.Fatalf("坑行错: %+v", pits[0])
	}
	// 委员会手改 action/expires 不被 learn 覆写
	if _, err := st.Pool().Exec(ctx,
		`UPDATE fin_venue_reject SET action='fix', expires_at=NULL WHERE venue='t'`); err != nil {
		t.Fatalf("manual fix: %v", err)
	}
	if err := st.LearnVenueReject(ctx, "t", "dci_subscribe", "code:-1102", time.Hour); err != nil {
		t.Fatalf("learn3: %v", err)
	}
	pits2, _ := st.ListVenueRejects(ctx, "t", "dci_subscribe")
	if pits2[0].Action != "fix" || pits2[0].ExpiresAt != nil || pits2[0].Hits != 3 {
		t.Fatalf("覆写锚破: %+v", pits2[0])
	}
}
