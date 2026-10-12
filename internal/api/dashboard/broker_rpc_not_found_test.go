package dashboard

// D-388 锚 C：store 层 ErrBrokerAccountNotFound → RPC CodeNotFound（0 行命中不再 2xx 假成功）。

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	v1 "nox/internal/gen/dashboardv1"
	"nox/internal/gen/dashboardv1/dashboardv1connect"
	"nox/internal/platform/testutil"
	"nox/internal/store/pgstore"

	"connectrpc.com/connect"
)

func d388Service(t *testing.T) *Service {
	t.Helper()
	s, err := pgstore.New(context.Background(), testutil.TestDSN(t, "NOX_TEST_PG_DSN"))
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	t.Cleanup(s.Close)
	if _, err := s.Pool().Exec(context.Background(), `TRUNCATE broker_accounts CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return &Service{store: s, bm: &BrokerManager{store: s}}
}

func TestD388_BrokerRPC_NotFound(t *testing.T) {
	svc := d388Service(t)
	ctx := context.Background()
	ghost := "ghost-venue"
	tr := true
	cases := []struct {
		name string
		call func() error
	}{
		{"alias", func() error {
			_, err := svc.SetBrokerAccountAlias(ctx, connect.NewRequest(&v1.SetBrokerAccountAliasRequest{Venue: ghost, Alias: "x"}))
			return err
		}},
		{"website", func() error {
			_, err := svc.SetBrokerWebsite(ctx, connect.NewRequest(&v1.SetBrokerWebsiteRequest{Venue: ghost, Website: "w"}))
			return err
		}},
		{"is_real", func() error {
			_, err := svc.SetBrokerAccountIsReal(ctx, connect.NewRequest(&v1.SetBrokerAccountIsRealRequest{Venue: ghost, IsReal: &tr}))
			return err
		}},
		{"commission", func() error {
			b := 1.0
			_, err := svc.SetBrokerCommission(ctx, connect.NewRequest(&v1.SetBrokerCommissionRequest{Venue: ghost, CommissionBps: &b}))
			return err
		}},
		{"disable", func() error {
			_, err := svc.SetBrokerDisabled(ctx, connect.NewRequest(&v1.SetBrokerDisabledRequest{Venue: ghost, Disabled: true}))
			return err
		}},
	}
	for _, c := range cases {
		err := c.call()
		if connect.CodeOf(err) != connect.CodeNotFound {
			t.Errorf("%s: want CodeNotFound, got %v", c.name, err)
		}
	}
}

// TestD388_SetBrokerDisabled_WireE2E 锚 B 等价物（施工环境无浏览器/端口复用受限，
// 走真生成 handler + 真 HTTP+Connect JSON wire——与前端 dashboard.setBrokerDisabled
// 同一线路）：connected venue POST disabled=true → 200 + DB status=stopped；
// ghost venue → HTTP 404（CodeNotFound）。
func TestD388_SetBrokerDisabled_WireE2E(t *testing.T) {
	svc := d388Service(t)
	ctx := context.Background()
	if _, err := svc.store.Pool().Exec(ctx,
		`INSERT INTO broker_accounts (venue, venue_type, host, port, password, server, status)
		 VALUES ('vE2E', 'mt5', 'h', 443, 'p', 's', 'connected')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, h := dashboardv1connect.NewDashboardServiceHandler(svc)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	post := func(body string) (*http.Response, string) {
		resp, err := http.Post(srv.URL+"/nox.dashboard.v1.DashboardService/SetBrokerDisabled",
			"application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		defer resp.Body.Close()
		buf := new(strings.Builder)
		_, _ = io.Copy(buf, resp.Body)
		return resp, buf.String()
	}

	// 真实存在 venue：disable → 200 + DB 翻 stopped
	resp, body := post(`{"venue":"vE2E","disabled":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("disable vE2E: status=%d body=%s", resp.StatusCode, body)
	}
	var st string
	if err := svc.store.Pool().QueryRow(ctx,
		`SELECT status FROM broker_accounts WHERE venue='vE2E'`).Scan(&st); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if st != "stopped" {
		t.Fatalf("status=%s, want stopped", st)
	}

	// ghost venue：HTTP 404（CodeNotFound 映射）——硬化前这里是 200 假成功
	resp, body = post(`{"venue":"ghost-404","disabled":true}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("ghost venue: status=%d body=%s, want 404", resp.StatusCode, body)
	}
}
